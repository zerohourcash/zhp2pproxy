//go:build !windows

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func runPlatform(configPath string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runProxy(ctx, configPath)
}
