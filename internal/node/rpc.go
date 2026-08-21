package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"zhp2pproxy/internal/config"
	"zhp2pproxy/pkg/jsonrpc"
)

type Client struct {
	mu     sync.RWMutex
	url    string
	user   string
	pass   string
	cookie string
	client *http.Client
}

func NewClient(cfg config.Config) *Client {
	client := &Client{client: &http.Client{Timeout: 30 * time.Second}}
	client.Update(cfg)
	return client
}

// Update swaps local RPC connection settings without rebuilding handlers that
// already hold this client. It is used after automatic node-config repair.
func (c *Client) Update(cfg config.Config) {
	url := cfg.RPCURL
	if url == "" {
		url = fmt.Sprintf("http://%s:%d/", cfg.RPCHost, cfg.RPCPort)
	}
	c.mu.Lock()
	c.url = url
	c.user = cfg.RPCUser
	c.pass = cfg.RPCPass
	c.cookie = cfg.RPCCookiePath
	c.mu.Unlock()
}

func (c *Client) Call(ctx context.Context, req jsonrpc.Request) (json.RawMessage, *jsonrpc.Error) {
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
	url, user, pass, cookie := c.url, c.user, c.pass, c.cookie
	c.mu.RUnlock()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, &jsonrpc.Error{Code: -32603, Message: err.Error()}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
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

func readCookieAuth(path string) (string, string, error) {
	data, err := os.ReadFile(path)
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
