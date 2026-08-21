//go:build windows

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"

	"zhp2pproxy/internal/config"
)

const windowsServiceName = "ZHP2PProxy"

func runPlatform(configPath string) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if isService {
		return svc.Run(windowsServiceName, &windowsService{configPath: configPath})
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runProxy(ctx, configPath)
}

type windowsService struct {
	configPath string
}

func (service *windowsService) Execute(
	_ []string,
	requests <-chan svc.ChangeRequest,
	changes chan<- svc.Status,
) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}
	closeLog, err := configureWindowsServiceLog(service.configPath)
	if err != nil {
		return false, 1
	}
	defer closeLog()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runProxy(ctx, service.configPath)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: accepts}
	for {
		select {
		case err := <-done:
			if err != nil {
				log.Printf("[service] %v", err)
				return false, 1
			}
			return false, 0
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				changes <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case err := <-done:
					if err != nil {
						log.Printf("[service] shutdown: %v", err)
						return false, 1
					}
				case <-time.After(15 * time.Second):
					log.Printf("[service] shutdown timed out")
					return false, 1
				}
				return false, 0
			}
		}
	}
}

func configureWindowsServiceLog(configPath string) (func(), error) {
	resolvedConfigPath := config.ResolvePath(configPath)
	logPath := filepath.Join(filepath.Dir(resolvedConfigPath), "zhp2pproxy.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open service log %s: %w", logPath, err)
	}
	log.SetOutput(logFile)
	return func() {
		_ = logFile.Close()
	}, nil
}
