package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/zerohourcash/zhp2pproxy/internal/config"
	"github.com/zerohourcash/zhp2pproxy/internal/rpcaudit"
	"github.com/zerohourcash/zhp2pproxy/internal/securefs"
	"github.com/zerohourcash/zhp2pproxy/pkg/jsonrpc"
)

type Client struct {
	mu      sync.RWMutex
	url     string
	user    string
	pass    string
	cookie  string
	invalid string
	client  *http.Client
	audit   *rpcaudit.Tracker
}

func (c *Client) SetAudit(audit *rpcaudit.Tracker) {
	c.mu.Lock()
	c.audit = audit
	c.mu.Unlock()
}

func NewClient(cfg config.Config) *Client {
	client := &Client{client: &http.Client{Timeout: 30 * time.Second}}
	client.Update(cfg)
	return client
}

// Update swaps local RPC connection settings without rebuilding handlers that
// already hold this client. It is used after automatic node-config repair.
func (c *Client) Update(cfg config.Config) {
	endpoint := cfg.RPCURL
	if endpoint == "" {
		endpoint = fmt.Sprintf("http://%s:%d/", cfg.RPCHost, cfg.RPCPort)
	}
	invalid := validateRPCEndpoint(endpoint)
	c.mu.Lock()
	c.url = endpoint
	c.user = cfg.RPCUser
	c.pass = cfg.RPCPass
	c.cookie = cfg.RPCCookiePath
	c.invalid = invalid
	c.mu.Unlock()
}

func (c *Client) Call(ctx context.Context, req jsonrpc.Request) (result json.RawMessage, rpcErr *jsonrpc.Error) {
	started := time.Now()
	if req.JSONRPC == "" {
		req.JSONRPC = "2.0"
	}
	if len(req.Params) == 0 {
		req.Params = json.RawMessage("[]")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, &jsonrpc.Error{Code: -32600, Message: err.Error()}
	}
	c.mu.RLock()
	endpoint, user, pass, cookie, invalid, audit := c.url, c.user, c.pass, c.cookie, c.invalid, c.audit
	c.mu.RUnlock()
	if audit != nil {
		defer func() {
			audit.Record(rpcaudit.Event{
				Method: req.Method, Source: rpcaudit.Source(ctx), Route: "local-rpc",
				Target: rpcAuditTarget(endpoint), Duration: time.Since(started), Success: rpcErr == nil,
			})
		}()
	}
	if invalid != "" {
		return nil, &jsonrpc.Error{Code: -32603, Message: invalid}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, &jsonrpc.Error{Code: -32603, Message: err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if user == "" && pass == "" && cookie != "" {
		cookieUser, cookiePass, err := readCookieAuth(cookie)
		if err != nil {
			return nil, &jsonrpc.Error{Code: -32603, Message: err.Error()}
		}
		user, pass = cookieUser, cookiePass
	}
	if user != "" || pass != "" {
		httpReq.SetBasicAuth(user, pass)
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, &jsonrpc.Error{Code: -32603, Message: err.Error()}
	}
	defer resp.Body.Close()
	const maxRPCResponseBytes = 16 << 20
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxRPCResponseBytes+1))
	if err != nil {
		return nil, &jsonrpc.Error{Code: -32603, Message: err.Error()}
	}
	if len(respBody) > maxRPCResponseBytes {
		return nil, &jsonrpc.Error{Code: -32603, Message: "local rpc response exceeds 16 MiB limit"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		const maxErrorBodyBytes = 4096
		if len(respBody) > maxErrorBodyBytes {
			respBody = respBody[:maxErrorBodyBytes]
		}
		return nil, &jsonrpc.Error{Code: -32603, Message: fmt.Sprintf("local rpc http %d: %s", resp.StatusCode, string(respBody))}
	}
	var rpcResp jsonrpc.Response
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, &jsonrpc.Error{Code: -32603, Message: err.Error()}
	}
	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}
	return rpcResp.Result, nil
}

func rpcAuditTarget(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "invalid-local-rpc"
	}
	return parsed.Scheme + "://" + parsed.Host
}

func validateRPCEndpoint(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return "invalid RPC URL"
	}
	if parsed.User != nil {
		return "RPC URL must not contain credentials; use rpc_user and rpc_pass"
	}
	if parsed.Scheme == "https" {
		return ""
	}
	if parsed.Scheme != "http" {
		return "RPC URL must use https, except for a loopback node"
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return ""
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "unencrypted RPC is allowed only for a loopback node"
	}
	return ""
}

func readCookieAuth(path string) (string, string, error) {
	data, err := securefs.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read rpc cookie: %w", err)
	}
	user, pass, ok := strings.Cut(strings.TrimSpace(string(data)), ":")
	if !ok {
		return "", "", fmt.Errorf("read rpc cookie: invalid cookie format")
	}
	return user, pass, nil
}

func (c *Client) Ping(ctx context.Context) error {
	id := json.RawMessage("1")
	_, rpcErr := c.Call(ctx, jsonrpc.Request{JSONRPC: "2.0", ID: id, Method: "getblockcount", Params: json.RawMessage("[]")})
	if rpcErr != nil {
		return fmt.Errorf("rpc error %d: %s", rpcErr.Code, rpcErr.Message)
	}
	return nil
}
