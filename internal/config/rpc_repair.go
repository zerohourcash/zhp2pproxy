package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	managedRPCStart = "# BEGIN ZHP2PProxy managed RPC fallback"
	managedRPCEnd   = "# END ZHP2PProxy managed RPC fallback"
)

type LocalRPCRepairResult struct {
	NodeConfigPath     string
	NodeBackupPath     string
	ProxyBackupPath    string
	NodeConfigChanged  bool
	ProxyConfigChanged bool
}

// RepairLocalNodeRPC preserves unrelated node settings and installs a
// loopback-only RPC block that zhp2pproxy can read on the next config load.
func RepairLocalNodeRPC(proxyConfigPath, nodeConfigPath string) (Config, LocalRPCRepairResult, error) {
	result := LocalRPCRepairResult{NodeConfigPath: nodeConfigPath}
	if err := os.MkdirAll(filepath.Dir(nodeConfigPath), 0700); err != nil {
		return Config{}, result, fmt.Errorf("create node config directory: %w", err)
	}

	existing, err := os.ReadFile(nodeConfigPath)
	nodeExists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return Config{}, result, fmt.Errorf("read node config: %w", err)
	}
	if !localRPCSettingsReady(existing) {
		if nodeExists {
			result.NodeBackupPath = timestampedBackupPath(nodeConfigPath, "before-zhp2pproxy")
			if err := os.WriteFile(result.NodeBackupPath, existing, 0600); err != nil {
				return Config{}, result, fmt.Errorf("back up node config: %w", err)
			}
		}
		password := randomToken()
		if password == "" {
			return Config{}, result, fmt.Errorf("generate local RPC password")
		}
		updated := buildManagedRPCConfig(string(existing), password)
		if err := os.WriteFile(nodeConfigPath, []byte(updated), 0600); err != nil {
			return Config{}, result, fmt.Errorf("write node config: %w", err)
		}
		result.NodeConfigChanged = true
	}

	proxyData, err := os.ReadFile(proxyConfigPath)
	if err != nil {
		return Config{}, result, fmt.Errorf("read proxy config: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(proxyData, &raw); err != nil {
		return Config{}, result, fmt.Errorf("parse proxy config: %w", err)
	}
	desired := map[string]any{
		"node_config_path": nodeConfigPath,
		"rpc_url":          "",
		"rpc_host":         "127.0.0.1",
		"rpc_port":         float64(3889),
		"rpc_user":         "",
		"rpc_pass":         "",
		"rpc_cookie_path":  "",
	}
	for key, value := range desired {
		if fmt.Sprint(raw[key]) != fmt.Sprint(value) {
			raw[key] = value
			result.ProxyConfigChanged = true
		}
	}
	if result.ProxyConfigChanged {
		result.ProxyBackupPath = timestampedBackupPath(proxyConfigPath, "before-node-fallback")
		if err := os.WriteFile(result.ProxyBackupPath, proxyData, 0600); err != nil {
			return Config{}, result, fmt.Errorf("back up proxy config: %w", err)
		}
		updated, err := json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return Config{}, result, fmt.Errorf("encode proxy config: %w", err)
		}
		updated = append(updated, '\n')
		if err := os.WriteFile(proxyConfigPath, updated, 0600); err != nil {
			return Config{}, result, fmt.Errorf("write proxy config: %w", err)
		}
	}

	loaded, _, err := Load(proxyConfigPath)
	if err != nil {
		return Config{}, result, fmt.Errorf("reload repaired proxy config: %w", err)
	}
	return loaded, result, nil
}

func localRPCSettingsReady(data []byte) bool {
	values := parseNodeConfig(string(data))
	port, _ := strconv.Atoi(values["rpcport"])
	return values["server"] == "1" &&
		values["rpcbind"] == "127.0.0.1" &&
		values["rpcallowip"] == "127.0.0.1" &&
		port == 3889 &&
		values["rpcuser"] != "" &&
		values["rpcpassword"] != ""
}

func buildManagedRPCConfig(existing, password string) string {
	var preserved []string
	inManagedBlock := false
	for _, line := range strings.Split(strings.ReplaceAll(existing, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == managedRPCStart {
			inManagedBlock = true
			continue
		}
		if inManagedBlock {
			if trimmed == managedRPCEnd {
				inManagedBlock = false
			}
			continue
		}
		key, _, hasValue := strings.Cut(trimmed, "=")
		if hasValue && isManagedRPCKey(strings.ToLower(strings.TrimSpace(key))) {
			continue
		}
		preserved = append(preserved, line)
	}
	for len(preserved) > 0 && strings.TrimSpace(preserved[len(preserved)-1]) == "" {
		preserved = preserved[:len(preserved)-1]
	}
	managed := []string{
		managedRPCStart,
		"# Local RPC only. Generated after a failed local-node connectivity check.",
		"server=1",
		"rpcuser=zhp2pproxy",
		"rpcpassword=" + password,
		"rpcport=3889",
		"rpcbind=127.0.0.1",
		"rpcallowip=127.0.0.1",
		managedRPCEnd,
	}
	if len(preserved) > 0 {
		preserved = append(preserved, "")
	}
	return strings.Join(append(preserved, managed...), "\r\n") + "\r\n"
}

func parseNodeConfig(content string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
		}
	}
	return values
}

func isManagedRPCKey(key string) bool {
	switch key {
	case "server", "rpcuser", "rpcpassword", "rpcport", "rpcbind", "rpcallowip":
		return true
	default:
		return false
	}
}

func timestampedBackupPath(path, suffix string) string {
	return fmt.Sprintf("%s.%s-%s.bak", path, suffix, time.Now().Format("20060102-150405.000000000"))
}
