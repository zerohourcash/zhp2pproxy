package p2p

import (
	"reflect"
	"testing"

	"github.com/zerohourcash/zhp2pproxy/internal/config"
)

func TestBrowserListenAddrsEnableWebTransportAndWebRTCDirect(t *testing.T) {
	cfg := config.Default()
	want := []string{
		"/ip4/0.0.0.0/udp/10003/quic-v1/webtransport",
		"/ip6/::/udp/10003/quic-v1/webtransport",
		"/ip4/0.0.0.0/udp/10004/webrtc-direct",
		"/ip6/::/udp/10004/webrtc-direct",
	}
	if got := browserListenAddrs(cfg); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected browser listen addresses:\n got: %#v\nwant: %#v", got, want)
	}
	if got, wantProtocol := string(browserRPCProtocol(cfg)), "/zhcash/browser-rpc/1.0.0/zhcash-proxy-main"; got != wantProtocol {
		t.Fatalf("unexpected browser RPC protocol: got %q want %q", got, wantProtocol)
	}
}

func TestBrowserListenAddrsCanBeDisabledIndependently(t *testing.T) {
	cfg := config.Default()
	cfg.BrowserWebTransport = false
	got := browserListenAddrs(cfg)
	if len(got) != 2 || got[0] != "/ip4/0.0.0.0/udp/10004/webrtc-direct" {
		t.Fatalf("unexpected WebRTC-only addresses: %#v", got)
	}
	cfg.BrowserWebRTC = false
	if got := browserListenAddrs(cfg); len(got) != 0 {
		t.Fatalf("disabled browser transports still listen: %#v", got)
	}
}
