//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"zhp2pproxy/internal/config"
)

func handlePlatformAutostart(_ string, _ bool) error {
	return fmt.Errorf("autostart registration is supported only on Windows")
}

func runPlatform(configPath string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runProxy(ctx, configPath)
}

func recoverPlatformLocalNode(_ string, cfg config.Config) (localNodeRecovery, error) {
	return localNodeRecovery{Config: cfg}, nil
}
