package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func signedUpdateServer(t *testing.T, goos, goarch string, body []byte) (*httptest.Server, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(body)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/update.json":
			manifest := Manifest{
				Version:  "20260822-test",
				Sequence: 20260822010101,
				Downloads: map[string]Download{
					goos + "-" + goarch: {
						URL:    server.URL + "/binary",
						SHA256: hex.EncodeToString(hash[:]),
					},
				},
			}
			encoded, _ := json.Marshal(manifest)
			response.Write(encoded)
		case "/update.json.sig":
			var encoded []byte
			encoded, _ = signedManifestBytes(server.URL, goos, goarch, hash)
			response.Write([]byte(base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, encoded))))
		case "/binary":
			response.Write(body)
		default:
			http.NotFound(response, request)
		}
	}))
	return server, base64.StdEncoding.EncodeToString(publicKey)
}

func signedManifestBytes(baseURL, goos, goarch string, hash [32]byte) ([]byte, error) {
	return json.Marshal(Manifest{
		Version:  "20260822-test",
		Sequence: 20260822010101,
		Downloads: map[string]Download{
			goos + "-" + goarch: {
				URL:    baseURL + "/binary",
				SHA256: hex.EncodeToString(hash[:]),
			},
		},
	})
}

func TestWindowsUpdateIsAnnouncedWithoutReplacingExecutable(t *testing.T) {
	server, publicKey := signedUpdateServer(t, "windows", "amd64", []byte("windows executable"))
	defer server.Close()
	result, err := CheckAndApply(context.Background(), Options{
		CurrentSequence:  1,
		ManifestURL:      server.URL + "/update.json",
		PublicKeyBase64:  publicKey,
		GOOS:             "windows",
		GOARCH:           "amd64",
		AllowHTTPForTest: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Available || result.Installed || result.DownloadURL == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestLinuxUpdateIsVerifiedBackedUpAndAtomicallyInstalled(t *testing.T) {
	newBinary := []byte("#!/bin/sh\necho 'zhp2pproxy 20260822-test'\n")
	server, publicKey := signedUpdateServer(t, "linux", "amd64", newBinary)
	defer server.Close()
	dir := t.TempDir()
	executable := filepath.Join(dir, "zhp2pproxy")
	oldBinary := []byte("#!/bin/sh\necho old\n")
	if err := os.WriteFile(executable, oldBinary, 0755); err != nil {
		t.Fatal(err)
	}
	result, err := CheckAndApply(context.Background(), Options{
		CurrentSequence:  1,
		ManifestURL:      server.URL + "/update.json",
		PublicKeyBase64:  publicKey,
		ExecutablePath:   executable,
		GOOS:             "linux",
		GOARCH:           "amd64",
		AllowHTTPForTest: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Installed {
		t.Fatalf("update was not installed: %+v", result)
	}
	installed, _ := os.ReadFile(executable)
	previous, _ := os.ReadFile(executable + ".previous")
	if string(installed) != string(newBinary) || string(previous) != string(oldBinary) {
		t.Fatal("installed or backup binary content is wrong")
	}
}

func TestTamperedManifestIsRejected(t *testing.T) {
	server, publicKey := signedUpdateServer(t, "windows", "amd64", []byte("binary"))
	defer server.Close()
	publicKeyBytes, _ := base64.StdEncoding.DecodeString(publicKey)
	publicKeyBytes[0] ^= 0xff
	_, err := CheckAndApply(context.Background(), Options{
		CurrentSequence:  1,
		ManifestURL:      server.URL + "/update.json",
		PublicKeyBase64:  base64.StdEncoding.EncodeToString(publicKeyBytes),
		GOOS:             "windows",
		GOARCH:           "amd64",
		AllowHTTPForTest: true,
	})
	if err == nil {
		t.Fatal("tampered signature was accepted")
	}
}

func TestSecureHTTPClientRejectsHTTPSDowngradeRedirect(t *testing.T) {
	client := secureHTTPClient(&http.Client{}, false)
	request, err := http.NewRequest(http.MethodGet, "http://example.test/update.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := http.NewRequest(http.MethodGet, "https://example.test/update.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, []*http.Request{previous}); err == nil {
		t.Fatal("HTTPS to HTTP redirect was accepted")
	}
}

func TestLinuxUpdateRejectsSymlinkExecutable(t *testing.T) {
	server, publicKey := signedUpdateServer(t, "linux", "amd64", []byte("binary"))
	defer server.Close()
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.WriteFile(target, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "zhp2pproxy")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := CheckAndApply(context.Background(), Options{
		CurrentSequence:  1,
		ManifestURL:      server.URL + "/update.json",
		PublicKeyBase64:  publicKey,
		ExecutablePath:   link,
		GOOS:             "linux",
		GOARCH:           "amd64",
		AllowHTTPForTest: true,
	})
	if err == nil {
		t.Fatal("symlink executable was accepted")
	}
}

func TestOfficialGitHubReleaseURLPolicy(t *testing.T) {
	for _, accepted := range []string{
		"https://github.com/zerohourcash/zhp2pproxy/releases/latest/download/update.json",
		"https://github.com/zerohourcash/zhp2pproxy/releases/download/v1.0.0/zhp2pproxy-linux-amd64.bin",
	} {
		if !officialGitHubReleaseURL(accepted) {
			t.Fatalf("official release URL rejected: %s", accepted)
		}
	}
	for _, rejected := range []string{
		"https://github.com/another/zhp2pproxy/releases/download/v1/update.json",
		"https://github.com/zerohourcash/zhp2pproxy/issues/1",
		"https://github.com/zerohourcash/zhp2pproxy/releases/download/v1/update.json?x=1",
		"https://link.zeroscan.st/zhp2pproxy/update.json",
	} {
		if officialGitHubReleaseURL(rejected) {
			t.Fatalf("unofficial release URL accepted: %s", rejected)
		}
	}
}

func TestSecureHTTPClientRejectsRedirectOutsideGitHubAssets(t *testing.T) {
	client := secureHTTPClient(&http.Client{}, false)
	request, err := http.NewRequest(http.MethodGet, "https://evil.example/update.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := http.NewRequest(http.MethodGet, "https://github.com/zerohourcash/zhp2pproxy/releases/latest/download/update.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, []*http.Request{previous}); err == nil {
		t.Fatal("redirect outside official GitHub asset hosts was accepted")
	}
}
