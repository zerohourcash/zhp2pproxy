package proxy

import (
	"context"
	"encoding/json"
	"testing"

	"zhp2pproxy/internal/config"
	"zhp2pproxy/internal/node"
	"zhp2pproxy/pkg/jsonrpc"
)

type scanFallbackP2P struct {
	healthTestP2P
	calls int
	req   jsonrpc.Request
}

func (p *scanFallbackP2P) CallAny(_ context.Context, req jsonrpc.Request) (json.RawMessage, error) {
	p.calls++
	p.req = req
	return json.RawMessage(`{"success":true,"unspents":[]}`), nil
}

func TestWalletScanUsesP2POnlyAfterLocalRPCFailure(t *testing.T) {
	cfg := config.Default()
	cfg.RPCHost = "127.0.0.1"
	cfg.RPCPort = 1
	p2p := &scanFallbackP2P{}
	handler := NewHandler(cfg, node.NewClient(cfg), p2p, &Metrics{})
	req := jsonrpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "scantxoutset",
		Params:  json.RawMessage(`["start",["addr(ZF8xrYQFkfW2YYuWb6UWhY57TXLt6sgobZ)"]]`),
	}

	response := handler.handleOne(context.Background(), req)
	if response.Error != nil {
		t.Fatalf("bounded scan fallback failed: %+v", response.Error)
	}
	if p2p.calls != 1 || p2p.req.Method != "scantxoutset" {
		t.Fatalf("unexpected p2p fallback: calls=%d request=%+v", p2p.calls, p2p.req)
	}
}

func TestUnsafeScanIsNeverRelayedToP2P(t *testing.T) {
	cfg := config.Default()
	cfg.RPCHost = "127.0.0.1"
	cfg.RPCPort = 1
	p2p := &scanFallbackP2P{}
	handler := NewHandler(cfg, node.NewClient(cfg), p2p, &Metrics{})

	response := handler.handleOne(context.Background(), jsonrpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "scantxoutset",
		Params:  json.RawMessage(`["start",["raw(76a91400)"]]`),
	})
	if response.Error == nil {
		t.Fatal("local RPC failure should be returned for a scan that cannot be relayed")
	}
	if p2p.calls != 0 {
		t.Fatalf("unsafe scan reached p2p %d times", p2p.calls)
	}
}
