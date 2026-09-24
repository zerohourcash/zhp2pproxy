package p2p

import (
	"testing"

	ma "github.com/multiformats/go-multiaddr"

	"github.com/zerohourcash/zhp2pproxy/internal/config"
)

func TestEffectiveNATTraversalModes(t *testing.T) {
	enabled := true
	disabled := false
	for _, test := range []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{name: "adaptive", cfg: config.Config{OverlayMode: "adaptive", NATTraversalEnabled: &enabled}, want: true},
		{name: "explicit opt out", cfg: config.Config{OverlayMode: "adaptive", NATTraversalEnabled: &disabled}, want: false},
		{name: "legacy hole punching", cfg: config.Config{OverlayMode: "adaptive", EnableHolePunching: true}, want: true},
		{name: "forced relay", cfg: config.Config{OverlayMode: "adaptive", ForceRelay: true, NATTraversalEnabled: &enabled}, want: false},
		{name: "relay only", cfg: config.Config{OverlayMode: "relay-only", NATTraversalEnabled: &enabled}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := effectiveNATTraversal(test.cfg); got != test.want {
				t.Fatalf("effectiveNATTraversal() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestDirectDialAddrsRejectsRelayAndPrivateTargets(t *testing.T) {
	addresses := []ma.Multiaddr{
		ma.StringCast("/ip4/127.0.0.1/tcp/10001"),
		ma.StringCast("/ip4/192.168.1.20/tcp/10001"),
		ma.StringCast("/ip4/203.0.113.10/tcp/10001"),
		ma.StringCast("/dns4/relay.example.test/tcp/443"),
		ma.StringCast("/ip4/203.0.113.11/tcp/10001/p2p/QmYwAPJzv5CZsnAzt8auVTLwzR4mE8uHKZV1fRNN7WzHnH/p2p-circuit"),
	}
	got := directDialAddrs(addresses)
	if len(got) != 1 {
		t.Fatalf("directDialAddrs() returned %d addresses, want 1: %v", len(got), got)
	}
	if got[0].String() != addresses[2].String() {
		t.Fatalf("unexpected direct addresses: %v", got)
	}
}

func TestConnectionTransportClassification(t *testing.T) {
	for address, want := range map[string]string{
		"/ip4/203.0.113.1/tcp/10001":                      "tcp",
		"/ip4/203.0.113.1/udp/10001/quic-v1":              "quic",
		"/ip4/203.0.113.1/udp/10003/quic-v1/webtransport": "webtransport",
		"/ip4/203.0.113.1/udp/10004/webrtc-direct":        "webrtc-direct",
		"/ip4/203.0.113.1/tcp/10001/p2p/peer/p2p-circuit": "circuit-relay",
	} {
		if got := connectionTransport(address); got != want {
			t.Fatalf("connectionTransport(%q) = %q, want %q", address, got, want)
		}
	}
}
