package config

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const DefaultPath = "proxy.json"

var executablePath = os.Executable
var runtimeGOOS = runtime.GOOS
var getenv = os.Getenv

type Config struct {
	Listen               string   `json:"listen"`
	P2PPort              int      `json:"p2p_port"`
	PublicEndpoint       string   `json:"public_endpoint"`
	RPCURL               string   `json:"rpc_url"`
	RPCHost              string   `json:"rpc_host"`
	RPCPort              int      `json:"rpc_port"`
	RPCUser              string   `json:"rpc_user"`
	RPCPass              string   `json:"rpc_pass"`
	RPCCookiePath        string   `json:"rpc_cookie_path"`
	NodeConfigPath       string   `json:"node_config_path"`
	BootstrapPeers       []string `json:"bootstrap_peers"`
	BootstrapURLs        []string `json:"bootstrap_urls"`
	RateLimit            float64  `json:"rate_limit"`
	RateBurst            int      `json:"rate_burst"`
	APIToken             string   `json:"api_token"`
	AdminToken           string   `json:"admin_token"`
	AllowedIPs           []string `json:"allowed_ips"`
	CORSAllowedOrigins   []string `json:"cors_allowed_origins"`
	ExposePeerInfo       bool     `json:"expose_peer_info"`
	P2PLoadBalance       bool     `json:"p2p_load_balance"`
	PeerStorePath        string   `json:"peer_store_path"`
	IdentityKeyPath      string   `json:"identity_key_path"`
	OverlayNetworkID     string   `json:"overlay_network_id"`
	OverlayPrivate       bool     `json:"overlay_private"`
	OverlayPSK           string   `json:"overlay_psk"`
	OverlayMode          string   `json:"overlay_mode"`
	ForceRelay           bool     `json:"force_relay"`
	RelayEnabled         bool     `json:"relay_enabled"`
	EnableIPv6           bool     `json:"enable_ipv6"`
	EnableQUIC           bool     `json:"enable_quic"`
	EnableNATPortMap     bool     `json:"enable_nat_port_map"`
	EnableHolePunching   bool     `json:"enable_hole_punching"`
	NodeP2PHost          string   `json:"node_p2p_host"`
	NodeP2PPort          int      `json:"node_p2p_port"`
	P2PProxyListen       string   `json:"p2p_proxy_listen"`
	NativeP2PProxy       bool     `json:"native_p2p_proxy"`
	MaxBodyBytes         int64    `json:"max_body_bytes"`
	LocalRPCPreferDirect bool     `json:"local_rpc_prefer_direct"`
}

func Default() Config {
	return Config{
		Listen:               "127.0.0.1:8081",
		P2PPort:              10001,
		PublicEndpoint:       "",
		RPCURL:               "",
		RPCHost:              "127.0.0.1",
		RPCPort:              3889,
		BootstrapPeers:       []string{},
		BootstrapURLs:        []string{"https://link.zeroscan.st/.well-known/zhp2pproxy/peers.json", "https://link.zeroscan.st/peers.json"},
		RateLimit:            20,
		RateBurst:            40,
		AllowedIPs:           []string{},
		CORSAllowedOrigins:   []string{"http://localhost:5173", "http://127.0.0.1:5173"},
		ExposePeerInfo:       false,
		P2PLoadBalance:       true,
		PeerStorePath:        "peers.json",
		IdentityKeyPath:      "identity.key",
		OverlayNetworkID:     "zhcash-proxy-main",
		OverlayPrivate:       true,
		OverlayMode:          "relay-first",
		ForceRelay:           true,
		RelayEnabled:         true,
		EnableIPv6:           true,
		EnableQUIC:           true,
		EnableNATPortMap:     false,
		EnableHolePunching:   false,
		NodeP2PHost:          "127.0.0.1",
		NodeP2PPort:          3888,
		P2PProxyListen:       "127.0.0.1:3890",
		NativeP2PProxy:       true,
		MaxBodyBytes:         1 << 20,
		LocalRPCPreferDirect: false,
	}
}

func Load(path string) (Config, bool, error) {
	path = ResolvePath(path)
	if runtimeGOOS == "windows" {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			if err := migrateLegacyWindowsState(path); err != nil {
				return Config{}, false, fmt.Errorf("migrate legacy Windows proxy state: %w", err)
			}
		}
	}
	cfg := Default()
	created := false
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		cfg.AdminToken = randomToken()
		if err := Save(path, cfg); err != nil {
			return cfg, false, fmt.Errorf("create default config: %w", err)
		}
		created = true
	} else if err != nil {
		return cfg, false, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, created, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, created, err
	}
	applyDefaults(&cfg)
	configDir := filepath.Dir(path)
	normalizeRelativePaths(&cfg, configDir)
	nodePathDiscovered, err := fillRPCFromNodeConfig(&cfg, configDir)
	if err != nil {
		return cfg, created, err
	}
	if nodePathDiscovered {
		if err := updateConfigFileField(path, "node_config_path", cfg.NodeConfigPath); err != nil {
			return cfg, created, err
		}
	}
	return cfg, created, nil
}

func ResolvePath(path string) string {
	if path != "" {
		if abs, err := filepath.Abs(path); err == nil {
			return abs
		}
		return path
	}
	for _, candidate := range defaultConfigCandidates() {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return defaultConfigCandidates()[0]
}

func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0644)
}

func normalizeRelativePaths(cfg *Config, baseDir string) {
	cfg.PeerStorePath = absFromBase(baseDir, cfg.PeerStorePath)
	cfg.IdentityKeyPath = absFromBase(baseDir, cfg.IdentityKeyPath)
	cfg.NodeConfigPath = absFromBase(baseDir, cfg.NodeConfigPath)
	cfg.RPCCookiePath = absFromBase(baseDir, cfg.RPCCookiePath)
}

func absFromBase(baseDir, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(baseDir, path)
}

func applyDefaults(cfg *Config) {
	def := Default()
	if cfg.Listen == "" {
		cfg.Listen = def.Listen
	}
	if cfg.RPCHost == "" {
		cfg.RPCHost = def.RPCHost
	}
	if cfg.RPCPort == 0 {
		cfg.RPCPort = def.RPCPort
	}
	if cfg.BootstrapURLs == nil {
		cfg.BootstrapURLs = def.BootstrapURLs
	}
	if cfg.RateLimit <= 0 {
		cfg.RateLimit = def.RateLimit
	}
	if cfg.RateBurst <= 0 {
		cfg.RateBurst = def.RateBurst
	}
	if cfg.PeerStorePath == "" {
		cfg.PeerStorePath = def.PeerStorePath
	}
	if cfg.IdentityKeyPath == "" {
		cfg.IdentityKeyPath = def.IdentityKeyPath
	}
	if cfg.OverlayNetworkID == "" {
		cfg.OverlayNetworkID = def.OverlayNetworkID
	}
	if cfg.OverlayMode == "" {
		cfg.OverlayMode = def.OverlayMode
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = def.MaxBodyBytes
	}
	if cfg.NodeP2PHost == "" {
		cfg.NodeP2PHost = def.NodeP2PHost
	}
	if cfg.NodeP2PPort == 0 {
		cfg.NodeP2PPort = def.NodeP2PPort
	}
	if cfg.P2PProxyListen == "" {
		cfg.P2PProxyListen = def.P2PProxyListen
	}
	if cfg.CORSAllowedOrigins == nil {
		cfg.CORSAllowedOrigins = def.CORSAllowedOrigins
	}
}

func fillRPCFromNodeConfig(cfg *Config, configDir string) (bool, error) {
	if cfg.RPCUser != "" && cfg.RPCPass != "" {
		return false, nil
	}
	path := cfg.NodeConfigPath
	discovered := false
	if path == "" {
		var ok bool
		path, ok = discoverNodeConfigPath(configDir)
		if !ok {
			return false, nil
		}
		cfg.NodeConfigPath = path
		discovered = true
	}
	values, err := readNodeConfig(path)
	if err != nil {
		return discovered, nil
	}
	if cfg.RPCUser == "" {
		cfg.RPCUser = values["rpcuser"]
	}
	if cfg.RPCPass == "" {
		cfg.RPCPass = values["rpcpassword"]
	}
	if cfg.RPCUser == "" && cfg.RPCPass == "" && cfg.RPCCookiePath == "" {
		cookiePath := filepath.Join(filepath.Dir(path), ".cookie")
		if _, err := os.Stat(cookiePath); err == nil {
			cfg.RPCCookiePath = cookiePath
		}
	}
	if cfg.RPCHost == "" && values["rpcconnect"] != "" {
		cfg.RPCHost = values["rpcconnect"]
	}
	if values["rpcport"] != "" && (cfg.RPCPort == 0 || cfg.RPCPort == Default().RPCPort) {
		port, err := strconv.Atoi(values["rpcport"])
		if err == nil {
			cfg.RPCPort = port
		}
	}
	return discovered, nil
}

func updateConfigFileField(path, key string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	values := make(map[string]any)
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
	}
	values[key] = value
	data, err = json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0644)
}

func discoverNodeConfigPath(configDir string) (string, bool) {
	for _, path := range nodeConfigCandidates(configDir) {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
	}
	return "", false
}

func nodeConfigCandidates(configDir string) []string {
	var candidates []string
	add := func(path string) {
		if path == "" {
			return
		}
		for _, existing := range candidates {
			if existing == path {
				return
			}
		}
		candidates = append(candidates, path)
	}
	add(filepath.Join(configDir, "zerohour.conf"))
	if exe, err := executablePath(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		add(filepath.Join(filepath.Dir(exe), "zerohour.conf"))
	}
	if cwd, err := os.Getwd(); err == nil {
		add(filepath.Join(cwd, "zerohour.conf"))
	}
	add(defaultNodeConfigPath())
	return candidates
}

func readNodeConfig(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	values := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(val)
	}
	return values, scanner.Err()
}

func defaultNodeConfigPath() string {
	home, _ := os.UserHomeDir()
	switch runtimeGOOS {
	case "windows":
		if appData := getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "ZHCASH", "zerohour.conf")
		}
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "ZHCASH", "zerohour.conf")
	}
	return filepath.Join(home, ".zerohour", "zerohour.conf")
}

func defaultConfigCandidates() []string {
	var candidates []string
	// Windows executables are commonly launched from Downloads or moved between
	// directories. Keep all runtime state beside the ZHCASH node data instead of
	// creating proxy.json, peers.json and identity.key beside a transient EXE.
	if runtimeGOOS == "windows" {
		if appData := getenv("APPDATA"); appData != "" {
			return []string{filepath.Join(appData, "ZHCASH", "zhp2pproxy", DefaultPath)}
		}
	}
	if exe, err := executablePath(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), DefaultPath))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, DefaultPath))
	}
	if dir, err := os.UserConfigDir(); err == nil {
		candidate := filepath.Join(dir, "zhp2pproxy", DefaultPath)
		if len(candidates) == 0 || candidates[0] != candidate {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		candidates = append(candidates, DefaultPath)
	}
	return candidates
}

func migrateLegacyWindowsState(targetConfig string) error {
	var candidates []string
	if exe, err := executablePath(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), DefaultPath))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, DefaultPath))
	}
	seen := make(map[string]struct{})
	for _, legacyConfig := range candidates {
		if legacyConfig == targetConfig {
			continue
		}
		if _, ok := seen[legacyConfig]; ok {
			continue
		}
		seen[legacyConfig] = struct{}{}
		data, err := os.ReadFile(legacyConfig)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			continue
		}
		if _, ok := raw["overlay_network_id"]; !ok {
			continue
		}
		if _, ok := raw["p2p_proxy_listen"]; !ok {
			continue
		}
		cfg := Default()
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}
		oldDir := filepath.Dir(legacyConfig)
		legacyIdentity := absFromBase(oldDir, cfg.IdentityKeyPath)
		legacyPeers := absFromBase(oldDir, cfg.PeerStorePath)
		cfg.IdentityKeyPath = "identity.key"
		cfg.PeerStorePath = "peers.json"
		if cfg.OverlayNetworkID == "zhcash-proxy-main" {
			cfg.OverlayPSK = ""
		}
		if err := Save(targetConfig, cfg); err != nil {
			return err
		}
		targetDir := filepath.Dir(targetConfig)
		if err := copyRuntimeFileIfPresent(legacyIdentity, filepath.Join(targetDir, "identity.key"), 0600); err != nil {
			return err
		}
		if err := copyRuntimeFileIfPresent(legacyPeers, filepath.Join(targetDir, "peers.json"), 0600); err != nil {
			return err
		}
		return nil
	}
	return nil
}

func copyRuntimeFileIfPresent(source, destination string, mode os.FileMode) error {
	if source == "" || source == destination {
		return nil
	}
	if _, err := os.Stat(destination); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := os.ReadFile(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, mode)
}
