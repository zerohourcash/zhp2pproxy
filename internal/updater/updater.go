package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"zhp2pproxy/internal/securefs"
)

const (
	maxManifestBytes = 1 << 20
	maxBinaryBytes   = 128 << 20
)

type Download struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Version     string              `json:"version"`
	Sequence    int64               `json:"sequence"`
	PublishedAt string              `json:"published_at"`
	Downloads   map[string]Download `json:"downloads"`
}

type Result struct {
	Available   bool
	Installed   bool
	Version     string
	Sequence    int64
	DownloadURL string
}

type Options struct {
	CurrentVersion   string
	CurrentSequence  int64
	ManifestURL      string
	PublicKeyBase64  string
	Interval         time.Duration
	InitialDelay     time.Duration
	HTTPClient       *http.Client
	ExecutablePath   string
	GOOS             string
	GOARCH           string
	AllowHTTPForTest bool
	Logf             func(string, ...any)
}

func Monitor(ctx context.Context, options Options, applied chan<- Result) {
	interval := options.Interval
	if interval < 5*time.Minute {
		interval = 15 * time.Minute
	}
	delay := options.InitialDelay
	if delay < 0 {
		delay = 0
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	var lastNotice int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			result, err := CheckAndApply(ctx, options)
			if err != nil {
				logf(options, "[update] check failed: %v", err)
			} else if result.Installed {
				select {
				case applied <- result:
				case <-ctx.Done():
				}
				return
			} else if result.Available && result.Sequence != lastNotice {
				lastNotice = result.Sequence
				logf(options, "[update] version %s is available for Windows", result.Version)
				logf(options, "[update] download and open: %s", result.DownloadURL)
			}
			timer.Reset(interval)
		}
	}
}

func CheckAndApply(ctx context.Context, options Options) (Result, error) {
	if options.CurrentSequence <= 0 {
		return Result{}, errors.New("current build has no monotonic update sequence")
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	client = secureHTTPClient(client, options.AllowHTTPForTest)
	manifestURL, err := secureURL(options.ManifestURL, options.AllowHTTPForTest)
	if err != nil {
		return Result{}, err
	}
	manifestBytes, err := fetch(ctx, client, manifestURL, maxManifestBytes)
	if err != nil {
		return Result{}, fmt.Errorf("download update manifest: %w", err)
	}
	signatureBytes, err := fetch(ctx, client, manifestURL+".sig", 4096)
	if err != nil {
		return Result{}, fmt.Errorf("download update signature: %w", err)
	}
	if err := verifyManifest(manifestBytes, signatureBytes, options.PublicKeyBase64); err != nil {
		return Result{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return Result{}, fmt.Errorf("decode update manifest: %w", err)
	}
	if manifest.Sequence <= options.CurrentSequence {
		return Result{}, nil
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return Result{}, errors.New("update manifest has an empty version")
	}
	goos := options.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := options.GOARCH
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	download, ok := manifest.Downloads[goos+"-"+goarch]
	if !ok {
		return Result{}, fmt.Errorf("update manifest has no build for %s-%s", goos, goarch)
	}
	downloadURL, err := secureURL(download.URL, options.AllowHTTPForTest)
	if err != nil {
		return Result{}, fmt.Errorf("invalid update download URL: %w", err)
	}
	result := Result{
		Available:   true,
		Version:     manifest.Version,
		Sequence:    manifest.Sequence,
		DownloadURL: downloadURL,
	}
	if goos == "windows" {
		return result, nil
	}
	if goos != "linux" {
		return result, nil
	}
	executablePath := options.ExecutablePath
	if executablePath == "" {
		executablePath, err = os.Executable()
		if err != nil {
			return Result{}, fmt.Errorf("resolve current executable: %w", err)
		}
	}
	if err := installLinuxBinary(ctx, client, executablePath, downloadURL, download.SHA256, manifest.Version); err != nil {
		return Result{}, err
	}
	result.Installed = true
	return result, nil
}

func verifyManifest(manifest, encodedSignature []byte, publicKeyBase64 string) error {
	publicKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyBase64))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("invalid pinned update public key")
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encodedSignature)))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("invalid update manifest signature encoding")
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), manifest, signature) {
		return errors.New("update manifest signature verification failed")
	}
	return nil
}

func secureURL(raw string, allowHTTP bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "", errors.New("invalid URL")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("URL credentials and fragments are forbidden")
	}
	if parsed.Scheme != "https" && !(allowHTTP && parsed.Scheme == "http") {
		return "", errors.New("update URL must use HTTPS")
	}
	return parsed.String(), nil
}

func secureHTTPClient(client *http.Client, allowHTTP bool) *http.Client {
	copyClient := *client
	previousRedirectCheck := client.CheckRedirect
	copyClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if _, err := secureURL(request.URL.String(), allowHTTP); err != nil {
			return fmt.Errorf("refuse insecure update redirect: %w", err)
		}
		if len(via) > 0 && via[0].URL.Scheme == "https" && request.URL.Scheme != "https" {
			return errors.New("refuse HTTPS downgrade redirect")
		}
		if previousRedirectCheck != nil {
			return previousRedirectCheck(request, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &copyClient
}

func fetch(ctx context.Context, client *http.Client, target string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, limit+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("response exceeds size limit")
	}
	return body, nil
}

func installLinuxBinary(ctx context.Context, client *http.Client, executablePath, downloadURL, expectedHash, version string) error {
	expected, err := hex.DecodeString(strings.TrimSpace(expectedHash))
	if err != nil || len(expected) != sha256.Size {
		return errors.New("update manifest contains an invalid SHA-256")
	}
	binary, err := fetch(ctx, client, downloadURL, maxBinaryBytes)
	if err != nil {
		return fmt.Errorf("download Linux update: %w", err)
	}
	actual := sha256.Sum256(binary)
	if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
		return errors.New("linux update SHA-256 mismatch")
	}
	executablePath, err = filepath.Abs(executablePath)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	info, err := os.Lstat(executablePath)
	if err != nil {
		return fmt.Errorf("inspect current executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to update a non-regular executable")
	}
	temporary, err := uniqueUpdatePath(executablePath)
	if err != nil {
		return fmt.Errorf("create update staging name: %w", err)
	}
	if err := writeExecutable(temporary, binary); err != nil {
		return err
	}
	defer securefs.Remove(temporary)
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(checkCtx, temporary, "-version").CombinedOutput()
	if err != nil || !strings.Contains(string(output), version) {
		return fmt.Errorf("downloaded executable failed version check: %s", strings.TrimSpace(string(output)))
	}
	currentBinary, err := securefs.ReadFile(executablePath)
	if err != nil {
		return fmt.Errorf("read current executable for backup: %w", err)
	}
	previous := executablePath + ".previous"
	if err := securefs.WriteFile(previous, currentBinary, info.Mode().Perm()); err != nil {
		return fmt.Errorf("back up current executable: %w", err)
	}
	if err := securefs.Rename(temporary, executablePath); err != nil {
		return fmt.Errorf("atomically activate update: %w", err)
	}
	return nil
}

func uniqueUpdatePath(executablePath string) (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return executablePath + ".update-new-" + hex.EncodeToString(random), nil
}

func writeExecutable(path string, body []byte) error {
	if err := securefs.WriteFileExclusive(path, body, 0755); err != nil {
		return fmt.Errorf("create update executable: %w", err)
	}
	return nil
}

func logf(options Options, format string, values ...any) {
	if options.Logf != nil {
		options.Logf(format, values...)
	}
}
