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
	"time"

	"zhp2pproxy/internal/config"
	"zhp2pproxy/pkg/jsonrpc"
)

type Client struct {
	url    string
	user   string
	pass   string
	cookie string
	client *http.Client
}

func NewClient(cfg config.Config) *Client {
	url := cfg.RPCURL
	if url == "" {
		url = fmt.Sprintf("http://%s:%d/", cfg.RPCHost, cfg.RPCPort)
	}
	return &Client{
		url:    url,
		user:   cfg.RPCUser,
		pass:   cfg.RPCPass,
		cookie: cfg.RPCCookiePath,
		client: &http.Client{Timeout: 30 * time.Second},
	}
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
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, &jsonrpc.Error{Code: -32603, Message: err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	user, pass := c.user, c.pass
	if user == "" && pass == "" && c.cookie != "" {
		cookieUser, cookiePass, err := readCookieAuth(c.cookie)
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
