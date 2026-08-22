package rpcaudit

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTrackerLogsCallsAndHourlyShapeWithoutPayloads(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	tracker := New(func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	})
	tracker.Record(Event{
		Method: "getblockcount", Source: "http-jsonrpc", Route: "local-rpc",
		Target: "http://127.0.0.1:3889", Duration: 12 * time.Millisecond, Success: true,
	})
	tracker.Record(Event{
		Method: "getblock", Source: "browser-peer:12D3KooW-test", Route: "overlay-rpc",
		Target: "peer-1", Duration: 25 * time.Millisecond, Success: false,
	})
	tracker.ReportNow()

	mu.Lock()
	got := strings.Join(lines, "\n")
	mu.Unlock()
	for _, expected := range []string{
		"[rpc] method=getblockcount source=http-jsonrpc route=local-rpc",
		"target=http://127.0.0.1:3889 status=ok duration_ms=12",
		"[rpc] method=getblock source=browser-peer:12D3KooW-test route=overlay-rpc",
		"requests=2 ok=1 errors=1",
		"routes=local-rpc:1/0,overlay-rpc:1/1",
		"sources=browser-peer:1,http-jsonrpc:1",
		"methods=getblock:1,getblockcount:1",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("audit output does not contain %q:\n%s", expected, got)
		}
	}
	if strings.Contains(got, "params") || strings.Contains(got, "password") {
		t.Fatalf("audit output contains payload-like data: %s", got)
	}
}

func TestSourceContextAndFieldSanitizing(t *testing.T) {
	ctx := WithSource(context.Background(), "browser websocket\nforged=true")
	if got := Source(ctx); got != "browser_websocket_forged_true" {
		t.Fatalf("Source() = %q", got)
	}
	if got := Source(context.Background()); got != defaultSource {
		t.Fatalf("default Source() = %q", got)
	}
}
