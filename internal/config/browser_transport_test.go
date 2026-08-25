package config

import "testing"

func TestValidateBrowserTransports(t *testing.T) {
	cfg := Default()
	if err := validateBrowserTransports(cfg); err != nil {
		t.Fatalf("default browser transports must validate: %v", err)
	}
	cfg.BrowserWebRTCPort = 10003
	if err := validateBrowserTransports(cfg); err == nil {
		t.Fatal("expected duplicate UDP port validation error")
	}
	cfg = Default()
	cfg.BrowserWebTransportPort = 70000
	if err := validateBrowserTransports(cfg); err == nil {
		t.Fatal("expected invalid WebTransport port validation error")
	}
}
