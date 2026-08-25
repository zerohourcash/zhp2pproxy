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
	"sync"
	"time"

	"github.com/zerohourcash/zhp2pproxy/internal/securefs"
)

const (
	maxManifestBytes = 1 << 20
	maxBinaryBytes   = 128 << 20
	officialRepoHost = "github.com"
	officialRepoPath = "/zerohourcash/zhp2pproxy/releases/"
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

type Status struct {
	Enabled           bool   `json:"enabled"`
	Mode              string `json:"mode"`
	Source            string `json:"source"`
	CurrentVersion    string `json:"current_version"`
	CurrentSequence   int64  `json:"current_sequence"`
	Checking          bool   `json:"checking"`
	LastChecked       string `json:"last_checked,omitempty"`
	LastSuccess       string `json:"last_success,omitempty"`
	LastError         string `json:"last_error,omitempty"`
	AvailableVersion  string `json:"available_version,omitempty"`
	AvailableSequence int64  `json:"available_sequence,omitempty"`
}

type StatusStore struct {
	mu     sync.RWMutex
	status Status
}

func NewStatusStore(enabled bool, currentVersion string, currentSequence int64, source, goos string) *StatusStore {
	mode := "disabled"
	if enabled {
		mode = "check-only"
		switch goos {
		case "linux":
			mode = "automatic"
		case "windows":
			mode = "notify"
		}
	}
	return &StatusStore{status: Status{
		Enabled: enabled, Mode: mode, Source: source,
		CurrentVersion: currentVersion, CurrentSequence: currentSequence,
	}}
}

// SetError exposes a startup/configuration error even when the monitor cannot
// be started. This makes disabled development builds distinguishable from a
// configured release whose update sequence is invalid.
func (s *StatusStore) SetError(err error) {
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	s.status.LastError = err.Error()
	s.mu.Unlock()
}

func (s *StatusStore) Snapshot() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *StatusStore) beginCheck() {
	s.mu.Lock()
	s.status.Checking = true
	s.status.LastChecked = time.Now().UTC().Format(time.RFC3339)
	s.mu.Unlock()
}

func (s *StatusStore) finish(result Result, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Checking = false
	if err != nil {
		s.status.LastError = err.Error()
		return
	}
	s.status.LastSuccess = time.Now().UTC().Format(time.RFC3339)
	s.status.LastError = ""
	if result.Available {
		s.status.AvailableVersion = result.Version
		s.status.AvailableSequence = result.Sequence
	} else {
		s.status.AvailableVersion = ""
		s.status.AvailableSequence = 0
	}
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
	Status           *StatusStore
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
			if options.Status != nil {
				options.Status.beginCheck()
			}
			result, err := CheckAndApply(ctx, options)
			if options.Status != nil {
				options.Status.finish(result, err)
			}
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
	if !options.AllowHTTPForTest && !officialGitHubReleaseURL(manifestURL) {
		return Result{}, errors.New("update manifest must come from the official zerohourcash/zhp2pproxy GitHub Releases repository")
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
	if !options.AllowHTTPForTest && !officialGitHubReleaseURL(downloadURL) {
		return Result{}, errors.New("update binary must come from the official zerohourcash/zhp2pproxy GitHub Releases repository")
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

func officialGitHubReleaseURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return parsed.Scheme == "https" &&
		strings.EqualFold(parsed.Hostname(), officialRepoHost) &&
		parsed.Port() == "" &&
		strings.HasPrefix(parsed.EscapedPath(), officialRepoPath) &&
		parsed.RawQuery == "" && parsed.Fragment == "" && parsed.User == nil
}

func officialUpdateRedirectURL(raw string, allowHTTP bool) bool {
	if allowHTTP {
		_, err := secureURL(raw, true)
		return err == nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return parsed.Port() == ""
	default:
		return false
	}
}

func secureHTTPClient(client *http.Client, allowHTTP bool) *http.Client {
	copyClient := *client
	previousRedirectCheck := client.CheckRedirect
	copyClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if !officialUpdateRedirectURL(request.URL.String(), allowHTTP) {
			return errors.New("refuse update redirect outside official GitHub release infrastructure")
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
