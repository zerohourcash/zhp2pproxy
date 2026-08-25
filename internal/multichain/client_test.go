package multichain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zerohourcash/zhp2pproxy/internal/config"
	"github.com/zerohourcash/zhp2pproxy/pkg/jsonrpc"
)

func TestClientCallsConfiguredChainAndReportsStatus(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request jsonrpc.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Method != "eth_getBalance" {
			t.Fatalf("unexpected method %s", request.Method)
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x2a"}`))
	}))
	defer upstream.Close()

	client, err := New([]config.ChainRPCConfig{{
		ID: "bsc-mainnet", Kind: "evm", RPCURLs: []string{upstream.URL}, Enabled: true,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	client.http = upstream.Client()
	result, rpcErr := client.Call(context.Background(), "bsc-mainnet", jsonrpc.Request{
		JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "eth_getBalance", Params: json.RawMessage(`["0x0000000000000000000000000000000000000001","latest"]`),
	})
	if rpcErr != nil || string(result) != `"0x2a"` {
		t.Fatalf("unexpected result=%s error=%+v", result, rpcErr)
	}
	status := client.Status()
	if len(status) != 1 || !status[0].Connected || status[0].Endpoint == "" {
		t.Fatalf("unexpected status %+v", status)
	}
}

func TestClientRejectsMethodsAndUnsignedBroadcastShape(t *testing.T) {
	client, err := New([]config.ChainRPCConfig{{
		ID: "bsc-mainnet", Kind: "evm", RPCURLs: []string{"https://example.test"}, Enabled: true,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := client.Call(context.Background(), "bsc-mainnet", jsonrpc.Request{Method: "personal_unlockAccount", Params: json.RawMessage(`[]`)}); rpcErr == nil || rpcErr.Code != -32601 {
		t.Fatalf("dangerous method was not rejected: %+v", rpcErr)
	}
	if _, rpcErr := client.Call(context.Background(), "bsc-mainnet", jsonrpc.Request{Method: "eth_sendRawTransaction", Params: json.RawMessage(`["not-signed-hex"]`)}); rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("invalid broadcast was not rejected: %+v", rpcErr)
	}
}

func TestEnvelopePolicyIsChainBounded(t *testing.T) {
	params, _ := json.Marshal([]Envelope{{Chain: "bsc-mainnet", Method: "eth_getBalance", Params: json.RawMessage(`[]`)}})
	if !RequestAllowed(jsonrpc.Request{Method: "multichain_rpc", Params: params}) {
		t.Fatal("safe multichain envelope was rejected")
	}
	params, _ = json.Marshal([]Envelope{{Chain: "bsc-mainnet", Method: "admin_addPeer", Params: json.RawMessage(`[]`)}})
	if RequestAllowed(jsonrpc.Request{Method: "multichain_rpc", Params: params}) {
		t.Fatal("unsafe multichain method was allowed")
	}
}

func TestCatalogMergeKeepsLocalEndpointFirstAndIsAtomic(t *testing.T) {
	client, err := New([]config.ChainRPCConfig{{
		ID: "bsc-mainnet", Kind: "evm", RPCURLs: []string{"https://local-choice.example"}, Enabled: true,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Add([]config.ChainRPCConfig{{
		ID: "bsc-mainnet", Kind: "evm", RPCURLs: []string{"https://catalog.example"}, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if got := client.chains["bsc-mainnet"].RPCURLs; len(got) != 2 || got[0] != "https://local-choice.example" {
		t.Fatalf("local endpoint priority was lost: %v", got)
	}
	if err := client.Add([]config.ChainRPCConfig{
		{ID: "new-chain", Kind: "evm", RPCURLs: []string{"https://new.example"}, Enabled: true},
		{ID: "bsc-mainnet", Kind: "solana", RPCURLs: []string{"https://conflict.example"}, Enabled: true},
	}); err == nil {
		t.Fatal("conflicting catalog was accepted")
	}
	if _, exists := client.chains["new-chain"]; exists {
		t.Fatal("catalog was partially applied before conflict rejection")
	}
}

func TestRejectsCredentialLikeEndpointQuery(t *testing.T) {
	if _, err := New([]config.ChainRPCConfig{{
		ID: "bsc-mainnet", Kind: "evm", RPCURLs: []string{"https://rpc.example/?apiKey=secret"}, Enabled: true,
	}}, nil); err == nil {
		t.Fatal("credential-bearing endpoint query was accepted")
	}
}
