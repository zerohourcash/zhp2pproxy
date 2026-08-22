package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"zhp2pproxy/internal/config"
	"zhp2pproxy/internal/node"
	"zhp2pproxy/internal/proxy"
	"zhp2pproxy/pkg/jsonrpc"
)

type browserRelayP2P struct {
	calls int
}

func (p *browserRelayP2P) CallAny(_ context.Context, _ jsonrpc.Request) (json.RawMessage, error) {
	p.calls++
	return json.RawMessage(`1680001`), nil
}
func (*browserRelayP2P) CallPeer(context.Context, string, jsonrpc.Request) (json.RawMessage, error) {
	return json.RawMessage(`1680001`), nil
}
func (*browserRelayP2P) Info() any                                 { return nil }
func (*browserRelayP2P) AdminState() any                           { return nil }
func (*browserRelayP2P) ConnectPeer(context.Context, string) error { return nil }
func (*browserRelayP2P) Publish(context.Context, []byte) error     { return nil }
func (*browserRelayP2P) PeerID() string                            { return "relay-peer" }
func (*browserRelayP2P) NetworkID() string                         { return "zhcash-proxy-main" }
func (*browserRelayP2P) ConnectedPeerCount() int                   { return 1 }
func (*browserRelayP2P) NodeP2PStatus() any                        { return nil }

func TestBrowserRPCAllowsWalletMethodsAndRejectsNodeControl(t *testing.T) {
	rpcBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonrpc.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":1680000,"id":1}`))
	}))
	defer rpcBackend.Close()

	cfg := config.Default()
	cfg.RPCURL = rpcBackend.URL
	cfg.CORSAllowedOrigins = []string{"https://wallet.zeroscan.st"}
	metrics := &proxy.Metrics{}
	handler := proxy.NewHandler(cfg, node.NewClient(cfg), nil, metrics)
	server := httptest.NewServer(New(cfg, handler, metrics))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/zhp2p/rpc"
	headers := http.Header{"Origin": []string{"https://wallet.zeroscan.st"}}
	conn, response, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		if response != nil {
			t.Fatalf("dial browser rpc: %v (HTTP %d)", err, response.StatusCode)
		}
		t.Fatalf("dial browser rpc: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "getblockcount", "params": []any{},
	}); err != nil {
		t.Fatal(err)
	}
	var allowed jsonrpc.Response
	if err := conn.ReadJSON(&allowed); err != nil {
		t.Fatal(err)
	}
	if allowed.Error != nil || string(allowed.Result) != "1680000" {
		t.Fatalf("unexpected allowed response: %+v", allowed)
	}

	if err := conn.WriteJSON(map[string]any{
		"jsonrpc": "2.0", "id": 8, "method": "verifychain", "params": []any{},
	}); err != nil {
		t.Fatal(err)
	}
	var denied jsonrpc.Response
	if err := conn.ReadJSON(&denied); err != nil {
		t.Fatal(err)
	}
	if denied.Error == nil || denied.Error.Code != -32601 {
		t.Fatalf("node-control method was not denied: %+v", denied)
	}
}

func TestBrowserRPCRejectsUnknownOrigin(t *testing.T) {
	cfg := config.Default()
	cfg.CORSAllowedOrigins = []string{"https://wallet.zeroscan.st"}
	metrics := &proxy.Metrics{}
	handler := proxy.NewHandler(cfg, node.NewClient(cfg), nil, metrics)
	server := httptest.NewServer(New(cfg, handler, metrics))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/zhp2p/rpc"
	headers := http.Header{"Origin": []string{"https://evil.example"}}
	conn, response, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("unexpected cross-origin result: err=%v response=%v", err, response)
	}
}

func TestBrowserRPCRelaysWhenGatewayHasNoLocalNode(t *testing.T) {
	cfg := config.Default()
	cfg.RPCHost = "127.0.0.1"
	cfg.RPCPort = 1
	cfg.CORSAllowedOrigins = []string{"https://wallet.zeroscan.st"}
	metrics := &proxy.Metrics{}
	relay := &browserRelayP2P{}
	handler := proxy.NewHandler(cfg, node.NewClient(cfg), relay, metrics)
	server := httptest.NewServer(New(cfg, handler, metrics))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/zhp2p/rpc"
	headers := http.Header{"Origin": []string{"https://wallet.zeroscan.st"}}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(map[string]any{
		"jsonrpc": "2.0", "id": 9, "method": "getblockcount", "params": []any{},
	}); err != nil {
		t.Fatal(err)
	}
	var response jsonrpc.Response
	if err := conn.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil || string(response.Result) != "1680001" || relay.calls != 1 {
		t.Fatalf("request was not relayed: response=%+v calls=%d", response, relay.calls)
	}
}
