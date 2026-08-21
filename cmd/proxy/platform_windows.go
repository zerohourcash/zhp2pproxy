//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"

	"zhp2pproxy/internal/config"
)

const windowsServiceName = "ZHP2PProxy"
const windowsAutostartValue = "ZHP2PProxy"

func handlePlatformAutostart(configPath string, register bool) error {
	key, err := registry.OpenKey(
		registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`,
		registry.QUERY_VALUE|registry.SET_VALUE,
	)
	if err != nil {
		return fmt.Errorf("open current-user Windows autostart registry: %w", err)
	}
	defer key.Close()

	if !register {
		if err := key.DeleteValue(windowsAutostartValue); err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("remove Windows autostart: %w", err)
		}
		log.Printf("removed current-user Windows autostart")
		return nil
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable for Windows autostart: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("resolve absolute executable path: %w", err)
	}
	resolvedConfig := config.ResolvePath(configPath)
	command := quoteWindowsCommandArg(executable) + " -config " + quoteWindowsCommandArg(resolvedConfig)
	if err := key.SetStringValue(windowsAutostartValue, command); err != nil {
		return fmt.Errorf("write Windows autostart: %w", err)
	}
	stored, _, err := key.GetStringValue(windowsAutostartValue)
	if err != nil {
		return fmt.Errorf("verify Windows autostart: %w", err)
	}
	if stored != command {
		return fmt.Errorf("verify Windows autostart: stored command differs from requested command")
	}
	log.Printf("registered current-user Windows autostart: %s", command)
	return nil
}

func quoteWindowsCommandArg(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func runPlatform(configPath string) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if isService {
		return svc.Run(windowsServiceName, &windowsService{configPath: configPath})
	}
	if err := handlePlatformAutostart(configPath, true); err != nil {
		log.Printf("[autostart] warning: %v", err)
	}

	// Console mode is intentionally visible. Mirror output to a persistent file
	// so closing the window remains a simple way to stop the proxy without losing
	// diagnostics from the previous run.
	closeLog, err := configureWindowsLog(configPath, true)
	if err != nil {
		return err
	}
	defer closeLog()

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
	closeLog, err := configureWindowsLog(service.configPath, false)
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

func configureWindowsLog(configPath string, mirrorConsole bool) (func(), error) {
	resolvedConfigPath := config.ResolvePath(configPath)
	logPath := filepath.Join(filepath.Dir(resolvedConfigPath), "zhp2pproxy.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open service log %s: %w", logPath, err)
	}
	if mirrorConsole {
		log.SetOutput(io.MultiWriter(os.Stdout, logFile))
	} else {
		log.SetOutput(logFile)
	}
	return func() {
		_ = logFile.Close()
	}, nil
}
