package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zerohourcash/zhp2pproxy/internal/config"
	"github.com/zerohourcash/zhp2pproxy/internal/proxy"
)

func TestMiddlewareRejectsUntrustedBrowserOrigin(t *testing.T) {
	cfg := config.Default()
	handler := middleware(cfg, &proxy.Metrics{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

func TestMiddlewareAllowsConfiguredOrigin(t *testing.T) {
	cfg := config.Default()
	handler := middleware(cfg, &proxy.Metrics{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
}

func TestClientIPIgnoresForwardedHeaderFromUntrustedSource(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.8:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.1")
	if got := clientIP(req, nil); got != "203.0.113.8" {
		t.Fatalf("clientIP = %q", got)
	}
	trusted := parseNetworks([]string{"203.0.113.0/24"})
	if got := clientIP(req, trusted); got != "198.51.100.1" {
		t.Fatalf("trusted proxy clientIP = %q", got)
	}
}
