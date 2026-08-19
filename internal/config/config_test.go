package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCreatesDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.json")
	cfg, created, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected config to be created")
	}
	if cfg.Listen == "" || cfg.RPCHost == "" || cfg.RPCPort == 0 {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if cfg.IdentityKeyPath != filepath.Join(dir, "identity.key") {
		t.Fatalf("identity key should be relative to config dir: %s", cfg.IdentityKeyPath)
	}
	if cfg.PeerStorePath != filepath.Join(dir, "peers.json") {
		t.Fatalf("peer store should be relative to config dir: %s", cfg.PeerStorePath)
	}
	if cfg.OverlayMode != "relay-first" || !cfg.ForceRelay {
		t.Fatalf("relay-first overlay defaults not applied: %+v", cfg)
	}
	if cfg.EnableNATPortMap || cfg.EnableHolePunching {
		t.Fatalf("nat punching must be disabled by default: %+v", cfg)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestResolvePathPrefersExecutableDirectory(t *testing.T) {
	dir := t.TempDir()
	oldExecutablePath := executablePath
	executablePath = func() (string, error) {
		return filepath.Join(dir, "zhp2pproxy"), nil
	}
	defer func() { executablePath = oldExecutablePath }()

	path := filepath.Join(dir, DefaultPath)
	if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := ResolvePath(""); got != path {
		t.Fatalf("ResolvePath() = %s, want %s", got, path)
	}
}

func TestManualRPCCredentialsOverrideNodeConfig(t *testing.T) {
	dir := t.TempDir()
	nodePath := filepath.Join(dir, "zerohour.conf")
	if err := os.WriteFile(nodePath, []byte("rpcuser=node\nrpcpassword=nodepass\nrpcport=3999\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.NodeConfigPath = nodePath
	cfg.RPCUser = "manual"
	cfg.RPCPass = "manualpass"
	path := filepath.Join(dir, "proxy.json")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RPCUser != "manual" || loaded.RPCPass != "manualpass" {
		t.Fatalf("manual credentials not preserved: %+v", loaded)
	}
}

func TestAutoDiscoversNodeConfigAndPersistsPath(t *testing.T) {
	dir := t.TempDir()
	nodePath := filepath.Join(dir, "zerohour.conf")
	if err := os.WriteFile(nodePath, []byte("rpcuser=node\nrpcpassword=nodepass\nrpcconnect=10.0.0.5\nrpcport=3999\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "proxy.json")
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeConfigPath != nodePath {
		t.Fatalf("NodeConfigPath = %s, want %s", cfg.NodeConfigPath, nodePath)
	}
	if cfg.RPCUser != "node" || cfg.RPCPass != "nodepass" {
		t.Fatalf("credentials not loaded: %+v", cfg)
	}
	if cfg.RPCHost != "127.0.0.1" {
		t.Fatalf("default rpc_host should not be overridden unless empty: %s", cfg.RPCHost)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["node_config_path"] != nodePath {
		t.Fatalf("node_config_path not persisted: %#v", raw["node_config_path"])
	}
}

func TestAutoDiscoversRPCCookie(t *testing.T) {
	dir := t.TempDir()
	nodePath := filepath.Join(dir, "zerohour.conf")
	if err := os.WriteFile(nodePath, []byte("rpcport=13999\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".cookie"), []byte("__cookie__:secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.NodeConfigPath = nodePath
	cfg.RPCUser = ""
	cfg.RPCPass = ""
	path := filepath.Join(dir, "proxy.json")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RPCCookiePath != filepath.Join(dir, ".cookie") {
		t.Fatalf("RPCCookiePath = %s", loaded.RPCCookiePath)
	}
	if loaded.RPCPort != 13999 {
		t.Fatalf("RPCPort = %d, want 13999", loaded.RPCPort)
	}
}

func TestManualCredentialsSkipNodeConfigDiscovery(t *testing.T) {
	dir := t.TempDir()
	nodePath := filepath.Join(dir, "zerohour.conf")
	if err := os.WriteFile(nodePath, []byte("rpcuser=node\nrpcpassword=nodepass\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.RPCHost = "rpc.example.org"
	cfg.RPCPort = 3889
	cfg.RPCUser = "remote"
	cfg.RPCPass = "remotepass"
	path := filepath.Join(dir, "proxy.json")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.NodeConfigPath != "" {
		t.Fatalf("node config should not be discovered for manual remote credentials: %s", loaded.NodeConfigPath)
	}
	if loaded.RPCUser != "remote" || loaded.RPCPass != "remotepass" || loaded.RPCHost != "rpc.example.org" {
		t.Fatalf("manual remote config not preserved: %+v", loaded)
	}
}
