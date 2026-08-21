package node

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"zhp2pproxy/internal/config"
)

func TestClientUpdateSwitchesRPCWithoutReplacingClient(t *testing.T) {
	server := func(height int, hits *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%d}`, height)
		}))
	}
	var firstHits, secondHits atomic.Int32
	first := server(1, &firstHits)
	defer first.Close()
	second := server(2, &secondHits)
	defer second.Close()

	cfg := config.Default()
	cfg.RPCURL = first.URL
	client := NewClient(cfg)
	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.RPCURL = second.URL
	client.Update(cfg)
	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if firstHits.Load() != 1 || secondHits.Load() != 1 {
		t.Fatalf("RPC client did not switch endpoints: first=%d second=%d", firstHits.Load(), secondHits.Load())
	}
}
