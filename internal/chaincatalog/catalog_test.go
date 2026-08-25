package chaincatalog

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/zerohourcash/zhp2pproxy/internal/config"
)

func signedCatalog(t *testing.T, catalog Catalog) ([]byte, []byte, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	return payload, []byte(signature), base64.StdEncoding.EncodeToString(publicKey)
}

func TestFetchAndOfflineCache(t *testing.T) {
	payload, signature, publicKey := signedCatalog(t, Catalog{Sequence: 7, Chains: []config.ChainRPCConfig{{
		ID: "bsc-mainnet", Kind: "evm", RPCURLs: []string{"https://bsc.example.test"}, Enabled: true,
	}}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chains.json.sig" {
			_, _ = w.Write(signature)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	cache := filepath.Join(t.TempDir(), "chains.json")
	options := Options{URL: server.URL + "/chains.json", CachePath: cache, PublicKeyBase64: publicKey, HTTPClient: server.Client(), AllowHTTPForTest: true}
	catalog, gotPayload, gotSignature, err := Fetch(context.Background(), options)
	if err != nil || catalog.Sequence != 7 {
		t.Fatalf("fetch catalog=%+v error=%v", catalog, err)
	}
	if err := SaveCache(cache, gotPayload, gotSignature); err != nil {
		t.Fatal(err)
	}
	cached, err := LoadCached(options)
	if err != nil || cached.Sequence != 7 {
		t.Fatalf("cached catalog=%+v error=%v", cached, err)
	}
}

func TestRejectsTamperingAndPrivateEndpoint(t *testing.T) {
	payload, signature, publicKey := signedCatalog(t, Catalog{Sequence: 1, Chains: []config.ChainRPCConfig{{
		ID: "bsc-mainnet", Kind: "evm", RPCURLs: []string{"https://127.0.0.1:8545"}, Enabled: true,
	}}})
	if _, err := decodeVerified(payload, signature, publicKey); err == nil {
		t.Fatal("private endpoint was accepted")
	}
	payload[0] ^= 1
	if _, err := decodeVerified(payload, signature, publicKey); err == nil {
		t.Fatal("tampered catalog was accepted")
	}
}
