package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepairLocalNodeRPCPreservesNodeConfigAndSynchronizesProxy(t *testing.T) {
	dir := t.TempDir()
	proxyPath := filepath.Join(dir, "proxy.json")
	nodePath := filepath.Join(dir, "ZHCASH", "zerohour.conf")
	if err := Save(proxyPath, Default()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(nodePath), 0700); err != nil {
		t.Fatal(err)
	}
	original := "addnode=seed.example\r\nserver=0\r\nrpcport=9999\r\n"
	if err := os.WriteFile(nodePath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, result, err := RepairLocalNodeRPC(proxyPath, nodePath)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NodeConfigChanged || !result.ProxyConfigChanged {
		t.Fatalf("repair result = %+v", result)
	}
	if _, err := os.Stat(result.NodeBackupPath); err != nil {
		t.Fatalf("missing node backup: %v", err)
	}
	if _, err := os.Stat(result.ProxyBackupPath); err != nil {
		t.Fatalf("missing proxy backup: %v", err)
	}
	data, err := os.ReadFile(nodePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "addnode=seed.example") || strings.Contains(content, "rpcport=9999") {
		t.Fatalf("unrelated settings were not preserved: %s", content)
	}
	if !strings.Contains(content, "txindex=1") || !strings.Contains(content, "addrindex=1") {
		t.Fatalf("transaction and address indexes were not enabled: %s", content)
	}
	if strings.ContainsAny(content, "#;") || cfg.RPCUser != "zhp2pproxy" || cfg.RPCPass == "" || cfg.RPCPort != 3889 {
		t.Fatalf("repaired config not loaded correctly: cfg=%+v content=%s", cfg, content)
	}
	if !result.AddressIndexChanged {
		t.Fatal("first address-index enablement was not reported")
	}

	_, second, err := RepairLocalNodeRPC(proxyPath, nodePath)
	if err != nil {
		t.Fatal(err)
	}
	if second.NodeConfigChanged || second.ProxyConfigChanged {
		t.Fatalf("second repair must be idempotent: %+v", second)
	}
}

func TestRepairLocalNodeRPCUpgradesDisabledIndexes(t *testing.T) {
	dir := t.TempDir()
	proxyPath := filepath.Join(dir, "proxy.json")
	nodePath := filepath.Join(dir, "ZHCASH", "zerohour.conf")
	if err := Save(proxyPath, Default()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(nodePath), 0700); err != nil {
		t.Fatal(err)
	}
	existing := strings.Join([]string{
		"server=1",
		"rpcuser=user",
		"rpcpassword=password",
		"rpcport=3889",
		"rpcbind=127.0.0.1",
		"rpcallowip=127.0.0.1",
		"txindex=0",
		"addrindex=0",
		"addnode=seed.example # remove this comment",
		"; remove this comment line too",
		"",
	}, "\r\n")
	if err := os.WriteFile(nodePath, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	_, result, err := RepairLocalNodeRPC(proxyPath, nodePath)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NodeConfigChanged {
		t.Fatal("expected index upgrade to change node config")
	}
	data, err := os.ReadFile(nodePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.Contains(content, "txindex=0") || strings.Count(content, "txindex=1") != 1 ||
		strings.Contains(content, "addrindex=0") || strings.Count(content, "addrindex=1") != 1 {
		t.Fatalf("indexes were not upgraded safely: %s", content)
	}
	if !strings.Contains(content, "addnode=seed.example") || strings.ContainsAny(content, "#;") {
		t.Fatalf("unrelated node setting was lost: %s", content)
	}
	if !result.AddressIndexChanged {
		t.Fatal("address index change was not reported")
	}
}

func TestRepairLocalNodeRPCMigratesLegacyManagedCommentsWithoutReindex(t *testing.T) {
	dir := t.TempDir()
	proxyPath := filepath.Join(dir, "proxy.json")
	nodePath := filepath.Join(dir, "ZHCASH", "zerohour.conf")
	if err := Save(proxyPath, Default()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(nodePath), 0700); err != nil {
		t.Fatal(err)
	}
	existing := strings.Join([]string{
		"# user comment",
		"addnode=seed.example ; inline comment",
		managedRPCStart,
		"# old generated comment",
		"server=1",
		"rpcuser=old",
		"rpcpassword=oldpassword",
		"rpcport=3889",
		"rpcbind=127.0.0.1",
		"rpcallowip=127.0.0.1",
		"txindex=1",
		"addrindex=1",
		managedRPCEnd,
		"",
	}, "\r\n")
	if err := os.WriteFile(nodePath, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	_, result, err := RepairLocalNodeRPC(proxyPath, nodePath)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NodeConfigChanged {
		t.Fatal("expected legacy comments to trigger a clean rewrite")
	}
	if result.AddressIndexChanged {
		t.Fatal("already-enabled address index must not request another reindex")
	}
	data, err := os.ReadFile(nodePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.ContainsAny(content, "#;") {
		t.Fatalf("comments remained in node config: %s", content)
	}
	if !strings.Contains(content, "addnode=seed.example") || strings.Count(content, "addrindex=1") != 1 {
		t.Fatalf("comment-free migration lost settings: %s", content)
	}
}
