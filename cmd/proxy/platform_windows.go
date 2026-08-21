//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
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
	relaunched, err := installAndRelaunchWindowsExecutable()
	if err != nil {
		// A read-only or managed machine may not allow self-installation. Keep the
		// downloaded EXE usable in place instead of turning that into a hard stop.
		log.Printf("[install] warning: %v", err)
	} else if relaunched {
		return nil
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

func installAndRelaunchWindowsExecutable() (bool, error) {
	current, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("resolve downloaded executable: %w", err)
	}
	current, err = filepath.Abs(current)
	if err != nil {
		return false, fmt.Errorf("resolve downloaded executable path: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, fmt.Errorf("resolve Windows user profile: %w", err)
	}
	installDir := filepath.Join(home, "zhp2pproxy")
	installed := filepath.Join(installDir, "zhp2pproxy.exe")
	if strings.EqualFold(filepath.Clean(current), filepath.Clean(installed)) {
		return false, nil
	}
	if err := os.MkdirAll(installDir, 0700); err != nil {
		return false, fmt.Errorf("create install directory %s: %w", installDir, err)
	}

	temporary := installed + ".new"
	if err := copyWindowsExecutable(current, temporary); err != nil {
		return false, err
	}
	previous := installed + ".previous.exe"
	hadInstalled := false
	if _, err := os.Stat(installed); err == nil {
		hadInstalled = true
		_ = os.Remove(previous)
		if err := os.Rename(installed, previous); err != nil {
			_ = os.Remove(temporary)
			return false, fmt.Errorf("replace installed executable %s (close its existing window first): %w", installed, err)
		}
	} else if !os.IsNotExist(err) {
		_ = os.Remove(temporary)
		return false, fmt.Errorf("inspect installed executable %s: %w", installed, err)
	}
	if err := os.Rename(temporary, installed); err != nil {
		if hadInstalled {
			_ = os.Rename(previous, installed)
		}
		_ = os.Remove(temporary)
		return false, fmt.Errorf("activate installed executable %s: %w", installed, err)
	}

	command := exec.Command(installed, os.Args[1:]...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return false, fmt.Errorf("start installed executable %s: %w", installed, err)
	}
	log.Printf("[install] installed to %s and continuing from the permanent copy", installed)
	return true, nil
}

func copyWindowsExecutable(sourcePath, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open downloaded executable %s: %w", sourcePath, err)
	}
	defer source.Close()
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0700)
	if err != nil {
		return fmt.Errorf("create installed executable %s: %w", destinationPath, err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		_ = os.Remove(destinationPath)
		return fmt.Errorf("copy executable to %s: %w", destinationPath, err)
	}
	if err := destination.Sync(); err != nil {
		_ = destination.Close()
		_ = os.Remove(destinationPath)
		return fmt.Errorf("flush installed executable %s: %w", destinationPath, err)
	}
	if err := destination.Close(); err != nil {
		_ = os.Remove(destinationPath)
		return fmt.Errorf("close installed executable %s: %w", destinationPath, err)
	}
	return nil
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
	runtimeDir := filepath.Dir(resolvedConfigPath)
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		return nil, fmt.Errorf("create Windows runtime directory %s: %w", runtimeDir, err)
	}
	logPath := filepath.Join(runtimeDir, "zhp2pproxy.log")
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
