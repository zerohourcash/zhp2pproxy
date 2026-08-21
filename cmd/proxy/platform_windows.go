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
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"

	"zhp2pproxy/internal/config"
)

const windowsServiceName = "ZHP2PProxy"
const windowsAutostartValue = "ZHP2PProxy"

const wmQueryEndSession = 0x0011

var (
	user32                    = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows           = user32.NewProc("EnumWindows")
	procGetWindowThreadProcID = user32.NewProc("GetWindowThreadProcessId")
	procPostMessageW          = user32.NewProc("PostMessageW")
)

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

type windowsNodeProcess struct {
	PID  uint32
	Name string
	Path string
}

func recoverPlatformLocalNode(proxyConfigPath string, cfg config.Config) (localNodeRecovery, error) {
	result := localNodeRecovery{Config: cfg}
	if cfg.RPCURL != "" || (cfg.RPCHost != "127.0.0.1" && !strings.EqualFold(cfg.RPCHost, "localhost")) {
		return result, nil
	}
	processes, err := findWindowsNodeProcesses()
	if err != nil {
		return result, err
	}
	nodeConfigPath := cfg.NodeConfigPath
	if nodeConfigPath == "" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return result, fmt.Errorf("APPDATA is unavailable")
		}
		nodeConfigPath = filepath.Join(appData, "ZHCASH", "zerohour.conf")
	}
	if len(processes) == 0 {
		if _, err := os.Stat(nodeConfigPath); os.IsNotExist(err) {
			return result, nil
		} else if err != nil {
			return result, fmt.Errorf("inspect node config %s: %w", nodeConfigPath, err)
		}
	}
	updated, repair, err := config.RepairLocalNodeRPC(proxyConfigPath, nodeConfigPath)
	if err != nil {
		return result, err
	}
	result.Config = updated
	result.Attempted = true
	result.NodeConfigPath = repair.NodeConfigPath
	result.NodeBackupPath = repair.NodeBackupPath
	result.ProxyBackupPath = repair.ProxyBackupPath
	result.NodeConfigChanged = repair.NodeConfigChanged
	result.ProxyConfigChanged = repair.ProxyConfigChanged
	if len(processes) > 0 {
		restarted, err := restartWindowsNodeProcesses(processes)
		if err != nil {
			return result, err
		}
		result.Restarted = restarted
	}
	return result, nil
}

func findWindowsNodeProcesses() ([]windowsNodeProcess, error) {
	wanted := map[string]struct{}{
		"zerohourd.exe":   {},
		"zerohour-qt.exe": {},
		"zhcashd.exe":     {},
		"zhcash-qt.exe":   {},
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("enumerate Windows processes: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, fmt.Errorf("read first Windows process: %w", err)
	}
	var found []windowsNodeProcess
	for {
		name := strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
		if _, ok := wanted[name]; ok {
			path, pathErr := windowsProcessImagePath(entry.ProcessID)
			if pathErr == nil && path != "" {
				found = append(found, windowsNodeProcess{PID: entry.ProcessID, Name: name, Path: path})
			}
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if err == syscall.ERROR_NO_MORE_FILES {
				break
			}
			return nil, fmt.Errorf("read Windows process list: %w", err)
		}
	}
	return found, nil
}

func windowsProcessImagePath(pid uint32) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer[:size]), nil
}

func restartWindowsNodeProcesses(processes []windowsNodeProcess) ([]string, error) {
	var restarted []string
	for _, process := range processes {
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, process.PID)
		if err != nil {
			return restarted, fmt.Errorf("open %s process %d for restart: %w", process.Name, process.PID, err)
		}
		stopErr := requestWindowsNodeStop(process)
		if stopErr != nil {
			windows.CloseHandle(handle)
			return restarted, stopErr
		}
		waitResult, waitErr := windows.WaitForSingleObject(handle, 180_000)
		windows.CloseHandle(handle)
		if waitErr != nil {
			return restarted, fmt.Errorf("wait for %s process %d: %w", process.Name, process.PID, waitErr)
		}
		if waitResult != windows.WAIT_OBJECT_0 {
			return restarted, fmt.Errorf("%s process %d did not stop safely within 3 minutes", process.Name, process.PID)
		}
		command := exec.Command(process.Path)
		command.Dir = filepath.Dir(process.Path)
		command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
		if err := command.Start(); err != nil {
			return restarted, fmt.Errorf("restart %s from %s: %w", process.Name, process.Path, err)
		}
		restarted = append(restarted, process.Path)
	}
	return restarted, nil
}

func requestWindowsNodeStop(process windowsNodeProcess) error {
	if process.Name == "zerohour-qt.exe" || process.Name == "zhcash-qt.exe" {
		sent, err := postQueryEndSession(process.PID)
		if err != nil {
			return fmt.Errorf("request graceful shutdown of %s process %d: %w", process.Name, process.PID, err)
		}
		if sent {
			return nil
		}
	}
	output, err := exec.Command("taskkill.exe", "/PID", strconv.FormatUint(uint64(process.PID), 10)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("stop %s process %d: %w (%s)", process.Name, process.PID, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func postQueryEndSession(pid uint32) (bool, error) {
	sent := false
	callback := syscall.NewCallback(func(window uintptr, _ uintptr) uintptr {
		var windowPID uint32
		procGetWindowThreadProcID.Call(window, uintptr(unsafe.Pointer(&windowPID)))
		if windowPID == pid {
			result, _, _ := procPostMessageW.Call(window, wmQueryEndSession, 0, 0)
			if result != 0 {
				sent = true
			}
		}
		return 1
	})
	result, _, callErr := procEnumWindows.Call(callback, 0)
	if result == 0 && callErr != nil && callErr != syscall.Errno(0) {
		return sent, callErr
	}
	return sent, nil
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
