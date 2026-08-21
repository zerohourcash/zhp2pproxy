package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"zhp2pproxy/internal/config"
	"zhp2pproxy/internal/node"
	"zhp2pproxy/internal/p2p"
	"zhp2pproxy/internal/proxy"
	"zhp2pproxy/internal/server"
)

var version = "dev"

type localNodeRecovery struct {
	Config             config.Config
	Attempted          bool
	NodeConfigPath     string
	NodeBackupPath     string
	ProxyBackupPath    string
	NodeConfigChanged  bool
	ProxyConfigChanged bool
	Restarted          []string
}

func main() {
	configPath := flag.String("config", "", "path to proxy.json; default: executable dir, cwd, then user config dir")
	showVersion := flag.Bool("version", false, "print version and exit")
	registerAutostart := flag.Bool("register-autostart", false, "register visible console startup for the current Windows user")
	unregisterAutostart := flag.Bool("unregister-autostart", false, "remove current Windows user autostart registration")
	flag.Parse()
	if *showVersion {
		log.Printf("zhp2pproxy %s", version)
		return
	}
	if *registerAutostart && *unregisterAutostart {
		log.Fatal("-register-autostart and -unregister-autostart cannot be used together")
	}
	if *registerAutostart || *unregisterAutostart {
		if err := handlePlatformAutostart(*configPath, *registerAutostart); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := runPlatform(*configPath); err != nil {
		log.Fatal(err)
	}
}

func runProxy(parent context.Context, configPath string) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	resolvedConfigPath := config.ResolvePath(configPath)
	cfg, created, err := config.Load(resolvedConfigPath)
	if err != nil {
		return fmt.Errorf("[config] %w", err)
	}
	if created {
		log.Printf("[config] generated %s; edit it if you need custom rpc credentials or bootstrap peers", resolvedConfigPath)
	} else {
		log.Printf("[config] loaded %s", resolvedConfigPath)
	}

	local := node.NewClient(cfg)
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	initialPingErr := local.Ping(pingCtx)
	if initialPingErr != nil {
		log.Printf("[node] local rpc unavailable at startup: %v", initialPingErr)
	} else {
		log.Printf("[node] local rpc ok")
	}
	pingCancel()

	p2pSvc, err := p2p.New(ctx, cfg, local)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("[p2p] %w", err)
	}
	defer func() {
		if err := p2pSvc.Close(); err != nil {
			log.Printf("[p2p] close: %v", err)
		}
	}()
	log.Printf("[p2p] peer id: %s", p2pSvc.PeerID())

	metrics := &proxy.Metrics{}
	handler := proxy.NewHandler(cfg, local, p2pSvc, metrics)
	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.New(cfg, handler, metrics),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("[server] listening on %s", cfg.Listen)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serveErr <- fmt.Errorf("[server] %w", err)
			return
		}
		serveErr <- nil
	}()
	go monitorLocalNode(ctx, resolvedConfigPath, cfg, local, initialPingErr)

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if err != nil {
			return err
		}
		return nil
	}
	log.Printf("[shutdown] stopping")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("[server] shutdown: %w", err)
	}
	return nil
}

func monitorLocalNode(ctx context.Context, configPath string, cfg config.Config, local *node.Client, initialErr error) {
	connected := initialErr == nil
	failures := 0
	if initialErr != nil {
		failures = 1
	}
	recoveryAttempted := false
	var nextRecoveryAttempt time.Time
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := local.Ping(pingCtx)
		cancel()
		if err == nil {
			failures = 0
			if !connected {
				log.Printf("[node] local rpc connected")
			}
			connected = true
			recoveryAttempted = false
			continue
		}
		failures++
		if connected {
			log.Printf("[node] local rpc connection lost: %v", err)
		}
		connected = false
		if recoveryAttempted || failures < 2 || time.Now().Before(nextRecoveryAttempt) {
			continue
		}
		nextRecoveryAttempt = time.Now().Add(time.Minute)
		recovery, repairErr := recoverPlatformLocalNode(configPath, cfg)
		if !recovery.Attempted {
			if repairErr != nil {
				log.Printf("[node] automatic RPC recovery failed: %v", repairErr)
			}
			continue
		}
		recoveryAttempted = repairErr == nil
		cfg = recovery.Config
		local.Update(cfg)
		if recovery.NodeConfigChanged {
			log.Printf("[node] wrote loopback-only RPC settings to %s", recovery.NodeConfigPath)
		}
		if recovery.NodeBackupPath != "" {
			log.Printf("[node] node config backup: %s", recovery.NodeBackupPath)
		}
		if recovery.ProxyBackupPath != "" {
			log.Printf("[node] proxy config backup: %s", recovery.ProxyBackupPath)
		}
		for _, executable := range recovery.Restarted {
			log.Printf("[node] restarted %s; waiting for RPC", executable)
		}
		if repairErr != nil {
			log.Printf("[node] automatic RPC recovery warning: %v", repairErr)
		}
	}
}
