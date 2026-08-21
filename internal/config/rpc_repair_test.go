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
	if strings.Count(content, managedRPCStart) != 1 || cfg.RPCUser != "zhp2pproxy" || cfg.RPCPass == "" || cfg.RPCPort != 3889 {
		t.Fatalf("repaired config not loaded correctly: cfg=%+v content=%s", cfg, content)
	}

	_, second, err := RepairLocalNodeRPC(proxyPath, nodePath)
	if err != nil {
		t.Fatal(err)
	}
	if second.NodeConfigChanged || second.ProxyConfigChanged {
		t.Fatalf("second repair must be idempotent: %+v", second)
	}
}
