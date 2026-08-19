package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"zhp2pproxy/internal/config"
	"zhp2pproxy/internal/node"
	"zhp2pproxy/internal/p2p"
	"zhp2pproxy/internal/proxy"
	"zhp2pproxy/internal/server"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "", "path to proxy.json; default: executable dir, cwd, then user config dir")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		log.Printf("zhp2pproxy %s", version)
		return
	}

	resolvedConfigPath := config.ResolvePath(*configPath)
	cfg, created, err := config.Load(resolvedConfigPath)
	if err != nil {
		log.Fatalf("[config] %v", err)
	}
	if created {
		log.Printf("[config] generated %s; edit it if you need custom rpc credentials or bootstrap peers", resolvedConfigPath)
	} else {
		log.Printf("[config] loaded %s", resolvedConfigPath)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	local := node.NewClient(cfg)
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	if err := local.Ping(pingCtx); err != nil {
		log.Printf("[node] local rpc unavailable at startup: %v", err)
	} else {
		log.Printf("[node] local rpc ok")
	}
	pingCancel()

	p2pSvc, err := p2p.New(ctx, cfg, local)
	if err != nil {
		log.Fatalf("[p2p] %v", err)
	}
	defer p2pSvc.Close()
	log.Printf("[p2p] peer id: %s", p2pSvc.PeerID())

	metrics := &proxy.Metrics{}
	handler := proxy.NewHandler(cfg, local, p2pSvc, metrics)
	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.New(cfg, handler, metrics),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("[server] listening on %s", cfg.Listen)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[server] %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("[shutdown] stopping")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("[server] shutdown: %v", err)
	}
}
