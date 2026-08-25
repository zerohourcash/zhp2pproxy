package multichain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zerohourcash/zhp2pproxy/internal/config"
	"github.com/zerohourcash/zhp2pproxy/pkg/jsonrpc"
)

const maxResponseBytes = 16 << 20

var chainIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

var evmMethods = map[string]bool{
	"eth_chainId": true, "net_version": true, "eth_blockNumber": true,
	"eth_getBalance": true, "eth_call": true, "eth_estimateGas": true,
	"eth_gasPrice": true, "eth_maxPriorityFeePerGas": true, "eth_feeHistory": true,
	"eth_getCode": true, "eth_getStorageAt": true, "eth_getTransactionCount": true,
	"eth_getBlockByNumber": true, "eth_getBlockByHash": true,
	"eth_getTransactionByHash": true, "eth_getTransactionReceipt": true,
	"eth_getLogs": true, "eth_sendRawTransaction": true,
}

var solanaMethods = map[string]bool{
	"getHealth": true, "getVersion": true, "getBalance": true,
	"getBlockHeight": true, "getLatestBlockhash": true, "getSignatureStatuses": true,
	"getSignaturesForAddress": true, "getTransaction": true,
	"getTokenAccountsByOwner": true, "getTokenAccountBalance": true,
	"getAccountInfo": true, "getMultipleAccounts": true,
	"getFeeForMessage": true, "simulateTransaction": true, "sendTransaction": true,
}

type Envelope struct {
	Chain  string          `json:"chain"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type Status struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Configured  bool   `json:"configured"`
	Connected   bool   `json:"connected"`
	Endpoint    string `json:"endpoint,omitempty"`
	LatencyMS   int64  `json:"latency_ms,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	LastChecked string `json:"last_checked,omitempty"`
}

type Client struct {
	chains map[string]config.ChainRPCConfig
	http   *http.Client
	mu     sync.RWMutex
	status map[string]Status
	logf   func(string, ...any)
}

func New(configs []config.ChainRPCConfig, logf func(string, ...any)) (*Client, error) {
	client := &Client{
		chains: make(map[string]config.ChainRPCConfig),
		http: &http.Client{
			Timeout: 8 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		status: make(map[string]Status),
		logf:   logf,
	}
	if err := client.Add(configs); err != nil {
		return nil, err
	}
	return client, nil
}

// Add validates and merges public chain endpoints. Existing locally configured
// endpoints stay first, so a remotely supplied catalog can improve resilience
// without overriding the machine owner's configuration.
func (c *Client) Add(configs []config.ChainRPCConfig) error {
	validated := make([]config.ChainRPCConfig, 0, len(configs))
	for _, chain := range configs {
		chain.ID = strings.ToLower(strings.TrimSpace(chain.ID))
		chain.Kind = strings.ToLower(strings.TrimSpace(chain.Kind))
		if !chain.Enabled {
			continue
		}
		if !chainIDPattern.MatchString(chain.ID) || (chain.Kind != "evm" && chain.Kind != "solana") {
			return fmt.Errorf("invalid multichain definition %q", chain.ID)
		}
		clean := make([]string, 0, len(chain.RPCURLs))
		for _, raw := range chain.RPCURLs {
			parsed, err := url.Parse(strings.TrimSpace(raw))
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return fmt.Errorf("chain %s RPC endpoint must be credential-free HTTPS", chain.ID)
			}
			clean = append(clean, parsed.String())
		}
		if len(clean) == 0 {
			return fmt.Errorf("chain %s has no HTTPS RPC endpoints", chain.ID)
		}
		chain.RPCURLs = clean
		validated = append(validated, chain)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	kinds := make(map[string]string, len(c.chains)+len(validated))
	for id, chain := range c.chains {
		kinds[id] = chain.Kind
	}
	for _, chain := range validated {
		if kind, ok := kinds[chain.ID]; ok && kind != chain.Kind {
			return fmt.Errorf("chain %s kind conflicts with existing configuration", chain.ID)
		}
		kinds[chain.ID] = chain.Kind
	}
	for _, chain := range validated {
		if existing, ok := c.chains[chain.ID]; ok {
			seen := make(map[string]bool, len(existing.RPCURLs)+len(chain.RPCURLs))
			for _, endpoint := range existing.RPCURLs {
				seen[endpoint] = true
			}
			for _, endpoint := range chain.RPCURLs {
				if !seen[endpoint] {
					existing.RPCURLs = append(existing.RPCURLs, endpoint)
					seen[endpoint] = true
				}
			}
			c.chains[chain.ID] = existing
			continue
		}
		c.chains[chain.ID] = chain
		c.status[chain.ID] = Status{ID: chain.ID, Kind: chain.Kind, Configured: true}
	}
	return nil
}

func DecodeEnvelope(req jsonrpc.Request) (Envelope, error) {
	if req.Method != "multichain_rpc" {
		return Envelope{}, errors.New("not a multichain request")
	}
	var values []Envelope
	if err := json.Unmarshal(req.Params, &values); err != nil || len(values) != 1 {
		return Envelope{}, errors.New("multichain_rpc expects one envelope parameter")
	}
	values[0].Chain = strings.ToLower(strings.TrimSpace(values[0].Chain))
	if len(values[0].Params) == 0 {
		values[0].Params = json.RawMessage("[]")
	}
	return values[0], nil
}

func RequestAllowed(req jsonrpc.Request) bool {
	envelope, err := DecodeEnvelope(req)
	if err != nil || !chainIDPattern.MatchString(envelope.Chain) || len(envelope.Params) > 512<<10 {
		return false
	}
	return methodAllowed("evm", envelope.Method) || methodAllowed("solana", envelope.Method)
}

func methodAllowed(kind, method string) bool {
	if kind == "evm" {
		return evmMethods[method]
	}
	if kind == "solana" {
		return solanaMethods[method]
	}
	return false
}

func validateBroadcast(kind, method string, params json.RawMessage) error {
	if method != "eth_sendRawTransaction" && method != "sendTransaction" {
		return nil
	}
	var values []string
	if err := json.Unmarshal(params, &values); err != nil || len(values) != 1 {
		return errors.New("broadcast requires exactly one signed transaction")
	}
	if len(values[0]) < 16 || len(values[0]) > 512<<10 {
		return errors.New("signed transaction size is invalid")
	}
	if kind == "evm" && (!strings.HasPrefix(values[0], "0x") || len(values[0])%2 != 0) {
		return errors.New("EVM transaction must be 0x-prefixed hex")
	}
	return nil
}

func (c *Client) CallEnvelope(ctx context.Context, req jsonrpc.Request) (json.RawMessage, *jsonrpc.Error) {
	envelope, err := DecodeEnvelope(req)
	if err != nil {
		return nil, &jsonrpc.Error{Code: -32602, Message: err.Error()}
	}
	return c.Call(ctx, envelope.Chain, jsonrpc.Request{
		JSONRPC: "2.0", ID: req.ID, Method: envelope.Method, Params: envelope.Params,
	})
}

func (c *Client) Call(ctx context.Context, chainID string, req jsonrpc.Request) (json.RawMessage, *jsonrpc.Error) {
	c.mu.RLock()
	chain, ok := c.chains[strings.ToLower(strings.TrimSpace(chainID))]
	c.mu.RUnlock()
	if !ok {
		return nil, &jsonrpc.Error{Code: -32602, Message: "chain is not configured"}
	}
	if !methodAllowed(chain.Kind, req.Method) {
		return nil, &jsonrpc.Error{Code: -32601, Message: "method is not allowed for this chain"}
	}
	if err := validateBroadcast(chain.Kind, req.Method, req.Params); err != nil {
		return nil, &jsonrpc.Error{Code: -32602, Message: err.Error()}
	}
	started := time.Now()
	type answer struct {
		result json.RawMessage
		err    error
		host   string
	}
	answers := make(chan answer, len(chain.RPCURLs))
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, endpoint := range chain.RPCURLs {
		go func(endpoint string) {
			result, err := c.callEndpoint(requestCtx, endpoint, req)
			answers <- answer{result: result, err: err, host: endpointHost(endpoint)}
		}(endpoint)
	}
	var lastErr error
	for range chain.RPCURLs {
		select {
		case <-ctx.Done():
			return nil, &jsonrpc.Error{Code: -32603, Message: ctx.Err().Error()}
		case got := <-answers:
			if got.err == nil {
				cancel()
				c.record(chain, true, got.host, time.Since(started), "")
				return got.result, nil
			}
			lastErr = got.err
		}
	}
	message := "all configured RPC endpoints are unavailable"
	if lastErr != nil {
		message = lastErr.Error()
	}
	c.record(chain, false, "", time.Since(started), message)
	return nil, &jsonrpc.Error{Code: -32603, Message: message}
}

func (c *Client) callEndpoint(ctx context.Context, endpoint string, req jsonrpc.Request) (json.RawMessage, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "zhp2pproxy-multichain/1.0")
	response, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return nil, errors.New("upstream response exceeds limit")
	}
	var rpcResponse jsonrpc.Response
	if err := json.Unmarshal(data, &rpcResponse); err != nil {
		return nil, errors.New("upstream returned invalid JSON-RPC")
	}
	if rpcResponse.Error != nil {
		return nil, fmt.Errorf("upstream RPC %d: %s", rpcResponse.Error.Code, rpcResponse.Error.Message)
	}
	return rpcResponse.Result, nil
}

func (c *Client) record(chain config.ChainRPCConfig, connected bool, endpoint string, latency time.Duration, message string) {
	status := Status{ID: chain.ID, Kind: chain.Kind, Configured: true, Connected: connected,
		Endpoint: endpoint, LatencyMS: latency.Milliseconds(), LastError: message, LastChecked: time.Now().UTC().Format(time.RFC3339)}
	c.mu.Lock()
	previous := c.status[chain.ID]
	c.status[chain.ID] = status
	c.mu.Unlock()
	if c.logf != nil && (previous.Connected != connected || previous.Endpoint != endpoint) {
		c.logf("[multichain] chain=%s connected=%t endpoint=%s latency_ms=%d error=%s", chain.ID, connected, endpoint, status.LatencyMS, message)
	}
}

func endpointHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Host
}

func (c *Client) ProbeAll(ctx context.Context) []Status {
	c.mu.RLock()
	chains := make(map[string]config.ChainRPCConfig, len(c.chains))
	for id, chain := range c.chains {
		chains[id] = chain
	}
	c.mu.RUnlock()
	var wg sync.WaitGroup
	for id, chain := range chains {
		wg.Add(1)
		go func(id string, chain config.ChainRPCConfig) {
			defer wg.Done()
			method := "eth_chainId"
			if chain.Kind == "solana" {
				method = "getHealth"
			}
			probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			_, _ = c.Call(probeCtx, id, jsonrpc.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method, Params: json.RawMessage("[]")})
		}(id, chain)
	}
	wg.Wait()
	return c.Status()
}

func (c *Client) Status() []Status {
	c.mu.RLock()
	result := make([]Status, 0, len(c.status))
	for _, status := range c.status {
		result = append(result, status)
	}
	c.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
