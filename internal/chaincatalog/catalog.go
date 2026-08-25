package chaincatalog

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/zerohourcash/zhp2pproxy/internal/config"
	"github.com/zerohourcash/zhp2pproxy/internal/securefs"
)

const maxCatalogBytes = 1 << 20

type Catalog struct {
	Sequence    int64                   `json:"sequence"`
	PublishedAt string                  `json:"published_at"`
	Chains      []config.ChainRPCConfig `json:"chains"`
}

type Options struct {
	URL              string
	CachePath        string
	PublicKeyBase64  string
	Refresh          time.Duration
	HTTPClient       *http.Client
	AllowHTTPForTest bool
	Logf             func(string, ...any)
}

// LoadCached reads only a previously signature-verified catalog. A missing or
// damaged cache is non-fatal: built-in/local configuration remains sufficient.
func LoadCached(options Options) (Catalog, error) {
	if strings.TrimSpace(options.CachePath) == "" {
		return Catalog{}, errors.New("catalog cache path is empty")
	}
	payload, err := securefs.ReadFile(options.CachePath)
	if err != nil {
		return Catalog{}, err
	}
	signature, err := securefs.ReadFile(options.CachePath + ".sig")
	if err != nil {
		return Catalog{}, err
	}
	return decodeVerified(payload, signature, options.PublicKeyBase64)
}

// Fetch downloads and verifies the ZeroScan control-plane catalog. The signed
// document may add public RPC endpoints, but never credentials or private/local
// network destinations.
func Fetch(ctx context.Context, options Options) (Catalog, []byte, []byte, error) {
	catalogURL, err := validateCatalogURL(options.URL, options.AllowHTTPForTest)
	if err != nil {
		return Catalog{}, nil, nil, err
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	securedClient := *client
	securedClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	payload, err := fetch(ctx, &securedClient, catalogURL, maxCatalogBytes)
	if err != nil {
		return Catalog{}, nil, nil, fmt.Errorf("download chain catalog: %w", err)
	}
	signature, err := fetch(ctx, &securedClient, catalogURL+".sig", 4096)
	if err != nil {
		return Catalog{}, nil, nil, fmt.Errorf("download chain catalog signature: %w", err)
	}
	catalog, err := decodeVerified(payload, signature, options.PublicKeyBase64)
	return catalog, payload, signature, err
}

func SaveCache(path string, payload, signature []byte) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("catalog cache path is empty")
	}
	if err := securefs.WriteFile(path, payload, 0600); err != nil {
		return err
	}
	if err := securefs.WriteFile(path+".sig", signature, 0600); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// Monitor refreshes asynchronously. Network/control-plane failure is logged
// and ignored; the proxy continues with local, built-in, and cached settings.
func Monitor(ctx context.Context, options Options, initialSequence int64, apply func(Catalog) error) {
	refresh := options.Refresh
	if refresh < 5*time.Minute {
		refresh = 15 * time.Minute
	}
	sequence := initialSequence
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			catalog, payload, signature, err := Fetch(ctx, options)
			if err != nil {
				logf(options, "[catalog] ZeroScan refresh unavailable; autonomous configuration remains active: %v", err)
			} else if catalog.Sequence < sequence {
				logf(options, "[catalog] rejected rollback sequence=%d current=%d", catalog.Sequence, sequence)
			} else if catalog.Sequence > sequence {
				if err := apply(catalog); err != nil {
					logf(options, "[catalog] rejected sequence=%d: %v", catalog.Sequence, err)
				} else if err := SaveCache(options.CachePath, payload, signature); err != nil {
					logf(options, "[catalog] applied sequence=%d but could not persist cache: %v", catalog.Sequence, err)
				} else {
					sequence = catalog.Sequence
					logf(options, "[catalog] applied signed ZeroScan chain catalog sequence=%d chains=%d", sequence, len(catalog.Chains))
				}
			}
			timer.Reset(refresh)
		}
	}
}

func decodeVerified(payload, encodedSignature []byte, publicKeyBase64 string) (Catalog, error) {
	publicKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyBase64))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return Catalog{}, errors.New("invalid pinned catalog public key")
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encodedSignature)))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return Catalog{}, errors.New("invalid catalog signature encoding")
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature) {
		return Catalog{}, errors.New("chain catalog signature verification failed")
	}
	var catalog Catalog
	if err := json.Unmarshal(payload, &catalog); err != nil {
		return Catalog{}, fmt.Errorf("decode chain catalog: %w", err)
	}
	if catalog.Sequence <= 0 || len(catalog.Chains) == 0 || len(catalog.Chains) > 64 {
		return Catalog{}, errors.New("chain catalog has invalid sequence or chain count")
	}
	for _, chain := range catalog.Chains {
		if !chain.Enabled || len(chain.RPCURLs) == 0 || len(chain.RPCURLs) > 16 {
			return Catalog{}, fmt.Errorf("catalog chain %q has invalid endpoint configuration", chain.ID)
		}
		for _, endpoint := range chain.RPCURLs {
			if err := validatePublicEndpoint(endpoint); err != nil {
				return Catalog{}, fmt.Errorf("catalog chain %q: %w", chain.ID, err)
			}
		}
	}
	return catalog, nil
}

func validateCatalogURL(raw string, allowHTTP bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid catalog URL")
	}
	if parsed.Scheme != "https" && !(allowHTTP && parsed.Scheme == "http") {
		return "", errors.New("catalog URL must use HTTPS")
	}
	return parsed.String(), nil
}

func validatePublicEndpoint(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("RPC endpoint must be credential-free HTTPS")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return errors.New("local RPC endpoint is forbidden in remote catalog")
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()) {
		return errors.New("private RPC endpoint is forbidden in remote catalog")
	}
	return nil
}

func fetch(ctx context.Context, client *http.Client, raw string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain")
	req.Header.Set("User-Agent", "zhp2pproxy-chain-catalog/1.0")
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("response exceeds limit")
	}
	return data, nil
}

func logf(options Options, format string, args ...any) {
	if options.Logf != nil {
		options.Logf(format, args...)
	}
}
