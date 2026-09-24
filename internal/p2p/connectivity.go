package p2p

import (
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	holepunch "github.com/libp2p/go-libp2p/p2p/protocol/holepunch"

	"github.com/zerohourcash/zhp2pproxy/internal/config"
)

// NATTraversalStatus explains whether traffic is direct or relayed without
// exposing physical IP addresses in the public diagnostics.
type NATTraversalStatus struct {
	Enabled                bool              `json:"enabled"`
	Reachability           string            `json:"reachability"`
	NATTypes               map[string]string `json:"nat_types,omitempty"`
	ConnectedPeers         int               `json:"connected_peers"`
	DirectPeers            int               `json:"direct_peers"`
	RelayedPeers           int               `json:"relayed_peers"`
	DirectConnections      int               `json:"direct_connections"`
	RelayedConnections     int               `json:"relayed_connections"`
	Transports             map[string]int    `json:"transports,omitempty"`
	HolePunchAttempts      uint64            `json:"hole_punch_attempts"`
	HolePunchSuccesses     uint64            `json:"hole_punch_successes"`
	HolePunchFailures      uint64            `json:"hole_punch_failures"`
	DirectUpgradeAttempts  uint64            `json:"direct_upgrade_attempts"`
	DirectUpgradeSuccesses uint64            `json:"direct_upgrade_successes"`
	DirectUpgradeFailures  uint64            `json:"direct_upgrade_failures"`
	LastTraversalUnix      int64             `json:"last_traversal_unix,omitempty"`
	LastTraversalPeer      string            `json:"last_traversal_peer,omitempty"`
	LastTraversalResult    string            `json:"last_traversal_result,omitempty"`
	LastTraversalError     string            `json:"last_traversal_error,omitempty"`
}

type connectivityTracker struct {
	mu sync.RWMutex

	reachability string
	natTypes     map[string]string

	holePunchAttempts      uint64
	holePunchSuccesses     uint64
	holePunchFailures      uint64
	directUpgradeAttempts  uint64
	directUpgradeSuccesses uint64
	directUpgradeFailures  uint64
	lastUnix               int64
	lastPeer               string
	lastResult             string
	lastError              string
}

func newConnectivityTracker() *connectivityTracker {
	return &connectivityTracker{
		reachability: network.ReachabilityUnknown.String(),
		natTypes:     make(map[string]string),
	}
}

// Trace implements holepunch.EventTracer.
func (t *connectivityTracker) Trace(evt *holepunch.Event) {
	if evt == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastUnix = time.Now().Unix()
	t.lastPeer = evt.Remote.String()
	switch value := evt.Evt.(type) {
	case *holepunch.HolePunchAttemptEvt:
		t.holePunchAttempts++
		t.lastResult = "hole-punch-attempt"
		t.lastError = ""
	case holepunch.HolePunchAttemptEvt:
		t.holePunchAttempts++
		t.lastResult = "hole-punch-attempt"
		t.lastError = ""
	case *holepunch.EndHolePunchEvt:
		t.finishHolePunch(value.Success, value.Error)
	case holepunch.EndHolePunchEvt:
		t.finishHolePunch(value.Success, value.Error)
	case *holepunch.DirectDialEvt:
		t.finishDirectDial(value.Success, value.Error)
	case holepunch.DirectDialEvt:
		t.finishDirectDial(value.Success, value.Error)
	}
}

func (t *connectivityTracker) finishHolePunch(success bool, failure string) {
	if success {
		t.holePunchSuccesses++
		t.lastResult = "hole-punch-success"
		t.lastError = ""
		log.Printf("[nat] direct connection established by hole punching peer=%s", t.lastPeer)
		return
	}
	t.holePunchFailures++
	t.lastResult = "hole-punch-failed"
	t.lastError = failure
	log.Printf("[nat] hole punching failed peer=%s error=%s; relay remains available", t.lastPeer, failure)
}

func (t *connectivityTracker) finishDirectDial(success bool, failure string) {
	if success {
		t.lastResult = "direct-dial-success"
		t.lastError = ""
		return
	}
	t.lastResult = "direct-dial-failed"
	t.lastError = failure
}

func (t *connectivityTracker) setReachability(value network.Reachability) {
	t.mu.Lock()
	t.reachability = value.String()
	t.mu.Unlock()
	log.Printf("[nat] reachability=%s", value)
}

func (t *connectivityTracker) setNATType(protocol network.NATTransportProtocol, value network.NATDeviceType) {
	t.mu.Lock()
	t.natTypes[protocol.String()] = value.String()
	t.mu.Unlock()
	log.Printf("[nat] transport=%s type=%s", protocol, value)
}

func (t *connectivityTracker) recordDirectUpgrade(id peer.ID, success bool, failure error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.directUpgradeAttempts++
	t.lastUnix = time.Now().Unix()
	t.lastPeer = id.String()
	if success {
		t.directUpgradeSuccesses++
		t.lastResult = "direct-upgrade-success"
		t.lastError = ""
		return
	}
	t.directUpgradeFailures++
	t.lastResult = "direct-upgrade-failed"
	if failure != nil {
		t.lastError = failure.Error()
	}
}

func (t *connectivityTracker) consumeEvent(value any) {
	switch evt := value.(type) {
	case event.EvtLocalReachabilityChanged:
		t.setReachability(evt.Reachability)
	case event.EvtNATDeviceTypeChanged:
		t.setNATType(evt.TransportProtocol, evt.NatDeviceType)
	}
}

func (t *connectivityTracker) snapshot(h host.Host, cfg config.Config) NATTraversalStatus {
	t.mu.RLock()
	status := NATTraversalStatus{
		Enabled:                effectiveNATTraversal(cfg),
		Reachability:           t.reachability,
		NATTypes:               make(map[string]string, len(t.natTypes)),
		Transports:             make(map[string]int),
		HolePunchAttempts:      t.holePunchAttempts,
		HolePunchSuccesses:     t.holePunchSuccesses,
		HolePunchFailures:      t.holePunchFailures,
		DirectUpgradeAttempts:  t.directUpgradeAttempts,
		DirectUpgradeSuccesses: t.directUpgradeSuccesses,
		DirectUpgradeFailures:  t.directUpgradeFailures,
		LastTraversalUnix:      t.lastUnix,
		LastTraversalPeer:      t.lastPeer,
		LastTraversalResult:    t.lastResult,
		LastTraversalError:     t.lastError,
	}
	for key, value := range t.natTypes {
		status.NATTypes[key] = value
	}
	t.mu.RUnlock()

	for _, id := range h.Network().Peers() {
		status.ConnectedPeers++
		direct, relayed, transports := peerPathSummary(h, id)
		if direct {
			status.DirectPeers++
		}
		if relayed && !direct {
			status.RelayedPeers++
		}
		for name, count := range transports {
			status.Transports[name] += count
		}
	}
	for _, conn := range h.Network().Conns() {
		if isRelayedConnection(conn) {
			status.RelayedConnections++
		} else {
			status.DirectConnections++
		}
	}
	if len(status.NATTypes) == 0 {
		status.NATTypes = nil
	}
	if len(status.Transports) == 0 {
		status.Transports = nil
	}
	return status
}

func effectiveNATTraversal(cfg config.Config) bool {
	if cfg.ForceRelay || cfg.OverlayMode == "relay-only" {
		return false
	}
	if cfg.NATTraversalEnabled != nil {
		return *cfg.NATTraversalEnabled
	}
	return cfg.EnableHolePunching
}

func peerPathSummary(h host.Host, id peer.ID) (direct bool, relayed bool, transports map[string]int) {
	transports = make(map[string]int)
	for _, conn := range h.Network().ConnsToPeer(id) {
		if isRelayedConnection(conn) {
			relayed = true
		} else {
			direct = true
		}
		transports[connectionTransport(conn.RemoteMultiaddr().String())]++
	}
	return direct, relayed, transports
}

func isRelayedConnection(conn network.Conn) bool {
	return conn.Stat().Limited || strings.Contains(conn.RemoteMultiaddr().String(), "/p2p-circuit")
}

func connectionTransport(address string) string {
	switch {
	case strings.Contains(address, "/p2p-circuit"):
		return "circuit-relay"
	case strings.Contains(address, "/webtransport"):
		return "webtransport"
	case strings.Contains(address, "/webrtc-direct"):
		return "webrtc-direct"
	case strings.Contains(address, "/quic-v1"):
		return "quic"
	case strings.Contains(address, "/tcp/"):
		return "tcp"
	default:
		return "other"
	}
}

func peerPathLabel(h host.Host, id peer.ID) (string, []string) {
	direct, relayed, transportCounts := peerPathSummary(h, id)
	path := "unknown"
	if direct {
		path = "direct"
	} else if relayed {
		path = "relay"
	}
	transports := make([]string, 0, len(transportCounts))
	for name := range transportCounts {
		transports = append(transports, name)
	}
	sort.Strings(transports)
	return path, transports
}
