package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/zerohourcash/zhp2pproxy/internal/chaincatalog"
	"github.com/zerohourcash/zhp2pproxy/internal/config"
	"github.com/zerohourcash/zhp2pproxy/internal/multichain"
	"github.com/zerohourcash/zhp2pproxy/internal/node"
	"github.com/zerohourcash/zhp2pproxy/internal/p2p"
	"github.com/zerohourcash/zhp2pproxy/internal/proxy"
	"github.com/zerohourcash/zhp2pproxy/internal/rpcaudit"
	"github.com/zerohourcash/zhp2pproxy/internal/server"
	"github.com/zerohourcash/zhp2pproxy/internal/updater"
)

var version = "dev"
var buildSequence = "0"

const updatePublicKey = "GnbiMTDyAI1NenxfEyJZihgFGsNUWmSVkke1Z6PweJA="

type localNodeRecovery struct {
	Config              config.Config
	Attempted           bool
	NodeConfigPath      string
	NodeBackupPath      string
	ProxyBackupPath     string
	NodeConfigChanged   bool
	ProxyConfigChanged  bool
	AddressIndexChanged bool
	Restarted           []string
}

func main() {
	configPath := flag.String("config", "", "path to proxy.json; default: executable dir, cwd, then user config dir")
	showVersion := flag.Bool("version", false, "print version and exit")
	registerAutostart := flag.Bool("register-autostart", false, "register visible console startup for the current Windows user")
	unregisterAutostart := flag.Bool("unregister-autostart", false, "remove current Windows user autostart registration")
	flag.Parse()
	if *showVersion {
		log.Printf("zhp2pproxy %s sequence=%s", version, buildSequence)
		return
	}
	if *registerAutostart && *unregisterAutostart {
		log.Fatal("-register-autostart and -unregister-autostart cannot be used together")
	}
	if *registerAutostart || *unregisterAutostart {
		//lint:ignore SA4023 the Windows build has a real fallible implementation;
		// staticcheck only sees the intentionally unsupported Unix implementation.
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
	log.Printf("[version] zhp2pproxy %s sequence=%s", version, buildSequence)

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

	rpcAudit := rpcaudit.New(log.Printf)
	rpcAudit.Start(ctx, time.Hour)
	defer rpcAudit.ReportNow()
	local := node.NewClient(cfg)
	local.SetAudit(rpcAudit)
	pingCtx, pingCancel := context.WithTimeout(rpcaudit.WithSource(ctx, "startup-probe"), 5*time.Second)
	initialPingErr := local.Ping(pingCtx)
	if initialPingErr != nil {
		log.Printf("[node] local rpc unavailable at startup: %v", initialPingErr)
	} else {
		log.Printf("[node] local rpc ok")
	}
	pingCancel()

	chains, err := multichain.New(cfg.MultichainRPC, log.Printf)
	if err != nil {
		return fmt.Errorf("[multichain] %w", err)
	}
	catalogSequence := int64(0)
	if !cfg.MultichainCatalogDisabled {
		cached, cacheErr := chaincatalog.LoadCached(chainCatalogOptions(cfg))
		if cacheErr == nil {
			if err := chains.Add(cached.Chains); err != nil {
				log.Printf("[catalog] ignored invalid cached catalog: %v", err)
			} else {
				catalogSequence = cached.Sequence
				log.Printf("[catalog] loaded signed offline cache sequence=%d chains=%d", cached.Sequence, len(cached.Chains))
			}
		} else if !os.IsNotExist(cacheErr) {
			log.Printf("[catalog] cached catalog unavailable: %v", cacheErr)
		}
	}
	p2pSvc, err := p2p.New(ctx, cfg, local, chains)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("[p2p] %w", err)
	}
	p2pSvc.SetAudit(rpcAudit)
	defer func() {
		if err := p2pSvc.Close(); err != nil {
			log.Printf("[p2p] close: %v", err)
		}
	}()
	log.Printf("[p2p] peer id: %s", p2pSvc.PeerID())

	metrics := &proxy.Metrics{}
	handler := proxy.NewHandler(cfg, local, p2pSvc, metrics)
	handler.SetMultichain(chains)
	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.New(cfg, handler, metrics),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
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
	go monitorMultichain(ctx, chains)
	if !cfg.MultichainCatalogDisabled {
		go chaincatalog.Monitor(ctx, chainCatalogOptions(cfg), catalogSequence, func(catalog chaincatalog.Catalog) error {
			if err := chains.Add(catalog.Chains); err != nil {
				return err
			}
			go chains.ProbeAll(ctx)
			return nil
		})
	} else {
		log.Printf("[catalog] signed ZeroScan chain catalog disabled; autonomous local configuration remains active")
	}
	updateApplied := startUpdateMonitor(ctx, cfg)

	select {
	case <-ctx.Done():
	case result := <-updateApplied:
		log.Printf("[update] installed %s; restarting through the process supervisor", result.Version)
		cancel()
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
	select {
	case result := <-updateApplied:
		return fmt.Errorf("update %s installed; restart required", result.Version)
	default:
	}
	return nil
}

func monitorMultichain(ctx context.Context, chains *multichain.Client) {
	chains.ProbeAll(ctx)
	log.Printf("[multichain] initial background probe complete chains=%d", len(chains.Status()))
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			chains.ProbeAll(ctx)
		}
	}
}

func chainCatalogOptions(cfg config.Config) chaincatalog.Options {
	return chaincatalog.Options{
		URL:             cfg.MultichainCatalogURL,
		CachePath:       cfg.MultichainCatalogCache,
		PublicKeyBase64: updatePublicKey,
		Refresh:         time.Duration(cfg.MultichainCatalogRefresh) * time.Second,
		Logf:            log.Printf,
	}
}

func startUpdateMonitor(ctx context.Context, cfg config.Config) <-chan updater.Result {
	applied := make(chan updater.Result, 1)
	sequence, err := strconv.ParseInt(buildSequence, 10, 64)
	if err != nil || sequence <= 0 || !cfg.AutoUpdateEnabled {
		if !cfg.AutoUpdateEnabled {
			log.Printf("[update] automatic update checks disabled by config")
		}
		return applied
	}
	go updater.Monitor(ctx, updater.Options{
		CurrentVersion:  version,
		CurrentSequence: sequence,
		ManifestURL:     cfg.UpdateManifestURL,
		PublicKeyBase64: updatePublicKey,
		Interval:        time.Duration(cfg.UpdateCheckInterval) * time.Second,
		InitialDelay:    20 * time.Second,
		Logf:            log.Printf,
	}, applied)
	return applied
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
		pingCtx, cancel := context.WithTimeout(rpcaudit.WithSource(ctx, "node-monitor"), 5*time.Second)
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
		if recovery.AddressIndexChanged {
			log.Printf("[node] enabled addrindex=1; node restart includes one-time -reindex-chainstate")
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
