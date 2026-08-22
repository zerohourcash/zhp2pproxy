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
	if cfg.OverlayPSK != "" {
		t.Fatalf("the shared default overlay must derive its PSK from network id, got %q", cfg.OverlayPSK)
	}
	if cfg.AdminToken == "" {
		t.Fatal("default config must contain a generated admin token")
	}
	if !cfg.AutoUpdateEnabled || cfg.UpdateManifestURL == "" || cfg.UpdateCheckInterval < 300 {
		t.Fatalf("secure update defaults not applied: %+v", cfg)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestLoadKeepsExplicitAutoUpdateOptOut(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.json")
	if err := os.WriteFile(path, []byte(`{"auto_update_enabled":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutoUpdateEnabled {
		t.Fatal("explicit automatic update opt-out was ignored")
	}
}

func TestLoadRepairsBlankAdminTokenAndFilePermissions(t *testing.T) {
	if runtimeGOOS == "windows" {
		t.Skip("POSIX permission bits are not authoritative on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.json")
	if err := os.WriteFile(path, []byte(`{"admin_token":""}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminToken == "" {
		t.Fatal("blank admin token was not repaired")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("config mode = %o, want 600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["admin_token"] != cfg.AdminToken {
		t.Fatal("generated admin token was not persisted")
	}
}

func TestLoadRejectsUnauthenticatedNonLoopbackHTTP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.json")
	if err := os.WriteFile(path, []byte(`{"listen":":8081","api_token":"","allowed_ips":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil {
		t.Fatal("unauthenticated non-loopback HTTP listener was accepted")
	}
}

func TestLoadAllowsProtectedNonLoopbackHTTP(t *testing.T) {
	for _, body := range []string{
		`{"listen":"0.0.0.0:8081","api_token":"test-token"}`,
		`{"listen":"[::]:8081","allowed_ips":["127.0.0.1"]}`,
	} {
		path := filepath.Join(t.TempDir(), "proxy.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(path); err != nil {
			t.Fatalf("protected listener rejected: %v", err)
		}
	}
}

func TestLoadPinsAndPersistsOfficialUpdateManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.json")
	if err := os.WriteFile(path, []byte(`{"update_manifest_url":"https://updates.example.test/update.json"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UpdateManifestURL != OfficialUpdateManifestURL {
		t.Fatalf("manifest URL = %q", cfg.UpdateManifestURL)
	}
	var stored map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["update_manifest_url"] != OfficialUpdateManifestURL {
		t.Fatalf("official manifest URL was not persisted: %#v", stored["update_manifest_url"])
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

func TestWindowsDefaultMigratesStateBesideNodeData(t *testing.T) {
	appData := t.TempDir()
	exeDir := t.TempDir()
	oldRuntimeGOOS := runtimeGOOS
	oldGetenv := getenv
	oldExecutablePath := executablePath
	runtimeGOOS = "windows"
	getenv = func(name string) string {
		if name == "APPDATA" {
			return appData
		}
		return ""
	}
	executablePath = func() (string, error) {
		return filepath.Join(exeDir, "zhp2pproxy.exe"), nil
	}
	defer func() {
		runtimeGOOS = oldRuntimeGOOS
		getenv = oldGetenv
		executablePath = oldExecutablePath
	}()

	legacy := Default()
	legacy.OverlayPSK = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := Save(filepath.Join(exeDir, DefaultPath), legacy); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(exeDir, "identity.key"), []byte("identity"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(exeDir, "peers.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	wantPath := filepath.Join(appData, "ZHCASH", "zhp2pproxy", DefaultPath)
	if got := ResolvePath(""); got != wantPath {
		t.Fatalf("ResolvePath() = %s, want %s", got, wantPath)
	}
	cfg, created, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("migrated config must not be reported as newly generated")
	}
	if cfg.OverlayPSK != "" {
		t.Fatalf("public overlay PSK was not normalized: %q", cfg.OverlayPSK)
	}
	if cfg.IdentityKeyPath != filepath.Join(filepath.Dir(wantPath), "identity.key") {
		t.Fatalf("identity path = %s", cfg.IdentityKeyPath)
	}
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(wantPath), "identity.key")); err != nil || string(data) != "identity" {
		t.Fatalf("identity was not migrated: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(wantPath), "peers.json")); err != nil {
		t.Fatalf("peers were not migrated: %v", err)
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
