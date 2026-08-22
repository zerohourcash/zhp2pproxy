package p2p

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/core/protocol"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
	"golang.org/x/time/rate"

	"zhp2pproxy/internal/config"
	"zhp2pproxy/internal/node"
	"zhp2pproxy/internal/proxy"
	"zhp2pproxy/internal/securefs"
	"zhp2pproxy/pkg/jsonrpc"
)

const (
	rpcProtocolBase   = "/zhcash/rpc/1.0.0"
	browserRPCBase    = "/zhcash/browser-rpc/1.0.0"
	nodeP2PBase       = "/zhcash/node-p2p/1.0.0"
	rpcStreamTimeout  = 5 * time.Second
	nodeStreamTimeout = 20 * time.Second
	bootstrapTimeout  = 10 * time.Second
	maxBootstrapPeers = 256
	maxAnnouncedAddrs = 32
)

type Service struct {
	cfg         config.Config
	host        host.Host
	browserHost host.Host
	pubsub      *pubsub.PubSub
	topic       *pubsub.Topic
	local       *node.Client
	overlay     overlaySpec
	tcpLn       net.Listener
	rpcPeerMu   sync.Mutex
	rpcPeerIx   int
	statsMu     sync.Mutex
	stats       map[peer.ID]*peerStats
	rpcMu       sync.Mutex
	rpcLimits   map[peer.ID]*rate.Limiter
	rpcGlobal   *rate.Limiter
	rpcSlots    chan struct{}
	scanGlobal  *rate.Limiter
	scanSlots   chan struct{}
	tunnelMu    sync.Mutex
	tunnels     map[peer.ID]int
	p2pHealth   NodeP2PStatus
	capMu       sync.RWMutex
	nodePeers   map[peer.ID]bool
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	closeOnce   sync.Once
}

type Info struct {
	PeerID             string   `json:"peer_id"`
	NetworkID          string   `json:"network_id"`
	Listen             []string `json:"listen,omitempty"`
	Peers              []string `json:"peers,omitempty"`
	Private            bool     `json:"private"`
	OverlayMode        string   `json:"overlay_mode"`
	ForceRelay         bool     `json:"force_relay"`
	Relay              bool     `json:"relay"`
	IPv6               bool     `json:"ipv6"`
	QUIC               bool     `json:"quic"`
	NATPortMap         bool     `json:"nat_port_map"`
	HolePunching       bool     `json:"hole_punching"`
	Rendezvous         string   `json:"rendezvous"`
	Topic              string   `json:"topic"`
	RPCProtocol        string   `json:"rpc_protocol"`
	NodeP2P            string   `json:"node_p2p_protocol"`
	P2PProxy           string   `json:"p2p_proxy_listen"`
	BrowserRPCProtocol string   `json:"browser_rpc_protocol,omitempty"`
	BrowserAddrs       []string `json:"browser_addrs,omitempty"`
}

type overlaySpec struct {
	NetworkID       string
	Rendezvous      string
	Topic           string
	RPCProtocol     protocol.ID
	NodeP2PProtocol protocol.ID
	Private         bool
}

type peerAnnounce struct {
	Type             string   `json:"type"`
	NetworkID        string   `json:"network_id"`
	PeerID           string   `json:"peer_id"`
	Addrs            []string `json:"addrs"`
	Time             int64    `json:"time"`
	NodeP2PAvailable *bool    `json:"node_p2p_available,omitempty"`
}

type bootstrapDocument struct {
	Peers          []string `json:"peers"`
	BootstrapPeers []string `json:"bootstrap_peers"`
}

type AdminState struct {
	NetworkID          string      `json:"network_id"`
	Private            bool        `json:"private"`
	PeerID             string      `json:"peer_id"`
	OverlayMode        string      `json:"overlay_mode"`
	ForceRelay         bool        `json:"force_relay"`
	Rendezvous         string      `json:"rendezvous"`
	Topic              string      `json:"topic"`
	RPCProtocol        string      `json:"rpc_protocol"`
	NodeP2PProtocol    string      `json:"node_p2p_protocol"`
	P2PProxyListen     string      `json:"p2p_proxy_listen"`
	BrowserRPCProtocol string      `json:"browser_rpc_protocol,omitempty"`
	BrowserAddrs       []string    `json:"browser_addrs,omitempty"`
	ListenAddrs        []string    `json:"listen_addrs"`
	BootstrapPeers     []string    `json:"bootstrap_peers"`
	BootstrapURLs      []string    `json:"bootstrap_urls"`
	ConnectedPeers     []AdminPeer `json:"connected_peers"`
	KnownPeers         []AdminPeer `json:"known_peers"`
}

type AdminPeer struct {
	ID               string   `json:"id"`
	Connectedness    string   `json:"connectedness"`
	Score            float64  `json:"score"`
	PublicIP         bool     `json:"public_ip"`
	Successes        uint64   `json:"successes"`
	Failures         uint64   `json:"failures"`
	AvgLatencyMS     int64    `json:"avg_latency_ms"`
	LastError        string   `json:"last_error,omitempty"`
	LastSuccessUnix  int64    `json:"last_success_unix,omitempty"`
	LastFailureUnix  int64    `json:"last_failure_unix,omitempty"`
	Addrs            []string `json:"addrs,omitempty"`
	Conns            []string `json:"conns,omitempty"`
	NodeP2PAvailable *bool    `json:"node_p2p_available,omitempty"`
}

type peerStats struct {
	successes   uint64
	failures    uint64
	avgLatency  time.Duration
	lastError   string
	lastSuccess time.Time
	lastFailure time.Time
}

type NodeP2PStatus struct {
	Enabled              bool   `json:"enabled"`
	Listening            bool   `json:"listening"`
	Listen               string `json:"listen"`
	ActiveTunnels        int    `json:"active_tunnels"`
	SuccessfulHandshakes uint64 `json:"successful_handshakes"`
	FailedHandshakes     uint64 `json:"failed_handshakes"`
	LastHandshakeUnix    int64  `json:"last_handshake_unix,omitempty"`
	LastRemotePeer       string `json:"last_remote_peer,omitempty"`
	LastLocalVersion     int32  `json:"last_local_protocol_version,omitempty"`
	LastRemoteVersion    int32  `json:"last_remote_protocol_version,omitempty"`
	LastError            string `json:"last_error,omitempty"`
	LocalNodeReachable   bool   `json:"local_node_reachable"`
	LocalNodeAddress     string `json:"local_node_address,omitempty"`
	LastLocalCheckUnix   int64  `json:"last_local_check_unix,omitempty"`
}

func (s *Service) PeerID() string {
	return s.host.ID().String()
}

func (s *Service) NetworkID() string {
	return s.overlay.NetworkID
}

func (s *Service) ConnectedPeerCount() int {
	return len(s.host.Network().Peers())
}

func (s *Service) NodeP2PStatus() any {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	status := s.p2pHealth
	status.ActiveTunnels = 0
	for _, count := range s.tunnels {
		status.ActiveTunnels += count
	}
	return status
}

func New(parent context.Context, cfg config.Config, local *node.Client) (_ *Service, returnErr error) {
	ctx, cancel := context.WithCancel(parent)
	defer func() {
		if returnErr != nil {
			cancel()
		}
	}()
	if _, err := decodeP2PMagic(cfg.NodeP2PMagic); err != nil {
		return nil, err
	}
	if !isLoopbackHost(cfg.NodeP2PHost) {
		return nil, errors.New("node_p2p_host must be localhost or a loopback IP")
	}
	priv, err := loadOrCreateIdentity(cfg.IdentityKeyPath)
	if err != nil {
		return nil, err
	}
	overlay := makeOverlaySpec(cfg)
	addrs := listenAddrs(cfg)
	limits := rcmgr.DefaultLimits
	libp2p.SetDefaultServiceLimits(&limits)
	resourceManager, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(limits.AutoScale()))
	if err != nil {
		return nil, fmt.Errorf("create libp2p resource manager: %w", err)
	}
	opts := []libp2p.Option{
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(addrs...),
		libp2p.EnableRelay(),
		libp2p.ResourceManager(resourceManager),
	}
	if cfg.OverlayPrivate {
		psk, err := overlayPSK(cfg)
		if err != nil {
			_ = resourceManager.Close()
			return nil, err
		}
		opts = append(opts, libp2p.PrivateNetwork(psk))
	}
	if cfg.EnableNATPortMap {
		opts = append(opts, libp2p.NATPortMap())
	}
	if cfg.EnableHolePunching {
		opts = append(opts, libp2p.EnableNATService(), libp2p.EnableHolePunching())
	}
	if cfg.RelayEnabled {
		resources := relayv2.DefaultResources()
		resources.Limit = &relayv2.RelayLimit{Duration: 6 * time.Hour, Data: 1 << 30}
		resources.MaxReservations = 128
		resources.MaxCircuits = 8
		resources.MaxReservationsPerIP = 4
		opts = append(opts, libp2p.EnableRelayService(relayv2.WithResources(resources)))
	}
	staticRelays := initialRelayCandidates(ctx, cfg)
	if cfg.ForceRelay || cfg.OverlayMode == "relay-only" || cfg.OverlayMode == "relay-first" {
		opts = append(opts, libp2p.ForceReachabilityPrivate())
		if len(staticRelays) > 0 {
			opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(staticRelays))
		}
	}
	h, err := libp2p.New(opts...)
	if err != nil {
		_ = resourceManager.Close()
		return nil, err
	}
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		_ = h.Close()
		return nil, err
	}
	topic, err := ps.Join(overlay.Topic)
	if err != nil {
		_ = h.Close()
		return nil, err
	}
	s := &Service{
		cfg: cfg, host: h, pubsub: ps, topic: topic, local: local, overlay: overlay,
		stats: make(map[peer.ID]*peerStats), rpcLimits: make(map[peer.ID]*rate.Limiter),
		rpcGlobal:  rate.NewLimiter(rate.Limit(cfg.P2PRPCRateLimit*float64(cfg.P2PRPCMaxConcurrent)), cfg.P2PRPCBurst*cfg.P2PRPCMaxConcurrent),
		rpcSlots:   make(chan struct{}, cfg.P2PRPCMaxConcurrent),
		scanGlobal: rate.NewLimiter(rate.Every(15*time.Second), 1), scanSlots: make(chan struct{}, 1),
		tunnels: make(map[peer.ID]int), nodePeers: make(map[peer.ID]bool),
		p2pHealth: NodeP2PStatus{Enabled: cfg.NativeP2PProxy, Listen: cfg.P2PProxyListen},
		cancel:    cancel,
	}
	if cfg.BrowserWebTransport || cfg.BrowserWebRTC {
		browserHost, err := newBrowserHost(cfg, priv)
		if err != nil {
			_ = h.Close()
			return nil, fmt.Errorf("create browser transport: %w", err)
		}
		s.browserHost = browserHost
		browserHost.SetStreamHandler(browserRPCProtocol(cfg), s.handleBrowserRPCStream)
		for _, addr := range s.browserMultiaddrs() {
			log.Printf("[browser-p2p] listening on %s", addr)
		}
	}
	h.SetStreamHandler(overlay.RPCProtocol, s.handleRPCStream)
	h.SetStreamHandler(overlay.NodeP2PProtocol, s.handleNodeP2PStream)
	// Bootstrap peers are external and may be offline or filtered. Do not hold
	// the local HTTP/RPC service startup on network dials; bounded background
	// attempts keep the proxy usable while connectivity recovers.
	s.run(func() { s.bootstrapLoop(ctx) })
	s.run(func() { s.pubsubDrain(ctx) })
	s.run(func() { s.announceLoop(ctx) })
	s.run(func() { s.peerCacheLoop(ctx) })
	if cfg.NativeP2PProxy {
		s.run(func() { s.startNodeP2PProxy(ctx) })
	}
	return s, nil
}

func (s *Service) run(fn func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn()
	}()
}

func (s *Service) bootstrapLoop(ctx context.Context) {
	connect := func() {
		if err := s.connectCachedAndBootstrap(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[p2p] bootstrap warning: %v", err)
		}
	}
	connect()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.ConnectedPeerCount() == 0 {
				connect()
			}
		}
	}
}

func initialRelayCandidates(ctx context.Context, cfg config.Config) []peer.AddrInfo {
	texts := append([]string(nil), cfg.BootstrapPeers...)
	if len(cfg.BootstrapURLs) > 0 {
		fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		type result struct {
			peers []string
		}
		results := make(chan result, len(cfg.BootstrapURLs))
		for _, endpoint := range cfg.BootstrapURLs {
			go func() {
				peers, _ := fetchBootstrapURL(fetchCtx, endpoint)
				results <- result{peers: peers}
			}()
		}
		for range cfg.BootstrapURLs {
			select {
			case got := <-results:
				texts = append(texts, got.peers...)
			case <-fetchCtx.Done():
				return uniqueRelayCandidates(parseStaticRelays(texts))
			}
		}
	}
	return uniqueRelayCandidates(parseStaticRelays(texts))
}

func uniqueRelayCandidates(candidates []peer.AddrInfo) []peer.AddrInfo {
	byID := make(map[peer.ID]*peer.AddrInfo)
	var order []peer.ID
	for _, candidate := range candidates {
		if existing := byID[candidate.ID]; existing != nil {
			existing.Addrs = appendUniqueMultiaddrs(existing.Addrs, candidate.Addrs...)
			continue
		}
		copyCandidate := candidate
		copyCandidate.Addrs = appendUniqueMultiaddrs(nil, candidate.Addrs...)
		byID[candidate.ID] = &copyCandidate
		order = append(order, candidate.ID)
	}
	out := make([]peer.AddrInfo, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out
}

func (s *Service) Close() error {
	var err error
	s.closeOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.tcpLn != nil {
			_ = s.tcpLn.Close()
		}
		s.wg.Wait()
		if s.topic != nil {
			_ = s.topic.Close()
		}
		if s.host != nil {
			err = s.host.Close()
		}
		if s.browserHost != nil {
			if browserErr := s.browserHost.Close(); err == nil {
				err = browserErr
			}
		}
	})
	return err
}

func (s *Service) Info() any {
	info := Info{
		PeerID:             s.host.ID().String(),
		NetworkID:          s.overlay.NetworkID,
		Private:            s.overlay.Private,
		OverlayMode:        s.cfg.OverlayMode,
		ForceRelay:         s.cfg.ForceRelay,
		Relay:              s.cfg.RelayEnabled,
		IPv6:               s.cfg.EnableIPv6,
		QUIC:               s.cfg.EnableQUIC,
		NATPortMap:         s.cfg.EnableNATPortMap,
		HolePunching:       s.cfg.EnableHolePunching,
		Rendezvous:         s.overlay.Rendezvous,
		Topic:              s.overlay.Topic,
		RPCProtocol:        string(s.overlay.RPCProtocol),
		NodeP2P:            string(s.overlay.NodeP2PProtocol),
		P2PProxy:           s.cfg.P2PProxyListen,
		BrowserRPCProtocol: string(browserRPCProtocol(s.cfg)),
	}
	info.BrowserAddrs = s.browserMultiaddrs()
	if s.cfg.ExposePeerInfo {
		for _, addr := range s.host.Addrs() {
			info.Listen = append(info.Listen, addr.Encapsulate(ma.StringCast("/p2p/"+s.host.ID().String())).String())
		}
		for _, p := range s.host.Network().Peers() {
			info.Peers = append(info.Peers, p.String())
		}
	}
	return info
}

func (s *Service) AdminState() any {
	state := AdminState{
		NetworkID:          s.overlay.NetworkID,
		Private:            s.overlay.Private,
		PeerID:             s.host.ID().String(),
		OverlayMode:        s.cfg.OverlayMode,
		ForceRelay:         s.cfg.ForceRelay,
		Rendezvous:         s.overlay.Rendezvous,
		Topic:              s.overlay.Topic,
		RPCProtocol:        string(s.overlay.RPCProtocol),
		NodeP2PProtocol:    string(s.overlay.NodeP2PProtocol),
		P2PProxyListen:     s.cfg.P2PProxyListen,
		BrowserRPCProtocol: string(browserRPCProtocol(s.cfg)),
		BrowserAddrs:       s.browserMultiaddrs(),
		BootstrapURLs:      append([]string(nil), s.cfg.BootstrapURLs...),
		BootstrapPeers:     append([]string(nil), s.cfg.BootstrapPeers...),
	}
	for _, addr := range s.host.Addrs() {
		state.ListenAddrs = append(state.ListenAddrs, addr.Encapsulate(ma.StringCast("/p2p/"+s.host.ID().String())).String())
	}
	seen := make(map[peer.ID]struct{})
	for _, id := range s.host.Network().Peers() {
		seen[id] = struct{}{}
		state.ConnectedPeers = append(state.ConnectedPeers, s.adminPeer(id))
	}
	for _, id := range s.host.Peerstore().Peers() {
		if id == s.host.ID() {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		state.KnownPeers = append(state.KnownPeers, s.adminPeer(id))
	}
	return state
}

func (s *Service) ConnectPeer(ctx context.Context, addr string) error {
	info, err := addrInfo(addr)
	if err != nil {
		return err
	}
	if info.ID == s.host.ID() {
		return errors.New("cannot connect to self")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return s.host.Connect(ctx, *info)
}

func (s *Service) adminPeer(id peer.ID) AdminPeer {
	score, publicIP, stats := s.peerScore(id)
	p := AdminPeer{
		ID:            id.String(),
		Connectedness: s.host.Network().Connectedness(id).String(),
		Score:         score,
		PublicIP:      publicIP,
	}
	if stats != nil {
		p.Successes = stats.successes
		p.Failures = stats.failures
		p.AvgLatencyMS = stats.avgLatency.Milliseconds()
		p.LastError = stats.lastError
		if !stats.lastSuccess.IsZero() {
			p.LastSuccessUnix = stats.lastSuccess.Unix()
		}
		if !stats.lastFailure.IsZero() {
			p.LastFailureUnix = stats.lastFailure.Unix()
		}
	}
	s.capMu.RLock()
	if available, known := s.nodePeers[id]; known {
		value := available
		p.NodeP2PAvailable = &value
	}
	s.capMu.RUnlock()
	for _, addr := range s.host.Peerstore().Addrs(id) {
		p.Addrs = append(p.Addrs, addr.String())
	}
	for _, conn := range s.host.Network().ConnsToPeer(id) {
		p.Conns = append(p.Conns, conn.RemoteMultiaddr().String())
	}
	return p
}

func (s *Service) Publish(ctx context.Context, data []byte) error {
	return s.topic.Publish(ctx, data)
}

func (s *Service) CallAny(ctx context.Context, req jsonrpc.Request) (json.RawMessage, error) {
	peers := s.rankedPeers()
	if len(peers) == 0 {
		return nil, errors.New("no connected p2p peers")
	}
	quorum := 1
	if proxy.P2PConsensusRequired(req.Method) && len(peers) >= s.cfg.P2PRPCQuorum {
		quorum = s.cfg.P2PRPCQuorum
	}
	type matchingResult struct {
		count int
		raw   json.RawMessage
	}
	matches := make(map[string]matchingResult)
	var last error
	for _, id := range peers {
		started := time.Now()
		result, err := s.callPeer(ctx, id, req)
		if err == nil {
			s.recordPeerResult(id, time.Since(started), nil)
			if quorum == 1 {
				return result, nil
			}
			canonical, canonicalErr := canonicalRPCResult(result)
			if canonicalErr != nil {
				last = fmt.Errorf("peer %s returned invalid JSON: %w", id, canonicalErr)
				continue
			}
			match := matches[string(canonical)]
			match.count++
			match.raw = result
			matches[string(canonical)] = match
			if match.count >= quorum {
				return match.raw, nil
			}
			last = fmt.Errorf("peer %s result did not reach quorum %d", id, quorum)
			continue
		}
		s.recordPeerResult(id, time.Since(started), err)
		last = err
	}
	if last == nil {
		last = errors.New("no usable p2p peers")
	}
	if quorum > 1 {
		return nil, fmt.Errorf("no matching p2p quorum for %s: %w", req.Method, last)
	}
	return nil, last
}

func canonicalRPCResult(result json.RawMessage) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(result))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return json.Marshal(value)
}

// CallPeer sends an RPC request to one specific connected overlay peer. It is
// intended for authenticated health diagnostics, not normal load balancing.
func (s *Service) CallPeer(ctx context.Context, peerID string, req jsonrpc.Request) (json.RawMessage, error) {
	id, err := peer.Decode(peerID)
	if err != nil {
		return nil, fmt.Errorf("invalid peer id: %w", err)
	}
	if s.host.Network().Connectedness(id) == network.NotConnected {
		return nil, fmt.Errorf("peer %s is not connected", id)
	}
	started := time.Now()
	result, err := s.callPeer(ctx, id, req)
	s.recordPeerResult(id, time.Since(started), err)
	return result, err
}

func (s *Service) nextPeerOffset(n int) int {
	if n <= 1 {
		return 0
	}
	s.rpcPeerMu.Lock()
	defer s.rpcPeerMu.Unlock()
	ix := s.rpcPeerIx % n
	s.rpcPeerIx++
	return ix
}

func (s *Service) callPeer(ctx context.Context, id peer.ID, req jsonrpc.Request) (json.RawMessage, error) {
	timeout := rpcTimeoutForMethod(req.Method)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	st, err := s.host.NewStream(ctx, id, s.overlay.RPCProtocol)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	_ = st.SetDeadline(time.Now().Add(timeout))
	requestData, err := json.Marshal(req)
	if err != nil {
		_ = st.Reset()
		return nil, err
	}
	if int64(len(requestData)) > s.cfg.MaxBodyBytes {
		_ = st.Reset()
		return nil, errors.New("p2p rpc request exceeds configured limit")
	}
	if _, err := st.Write(append(requestData, '\n')); err != nil {
		_ = st.Reset()
		return nil, err
	}
	var resp jsonrpc.Response
	if err := readLimitedJSON(st, s.cfg.P2PRPCResponseBytes, &resp); err != nil {
		_ = st.Reset()
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("peer %s rpc error %d: %s", id, resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

func (s *Service) rankedPeers() []peer.ID {
	peers := append([]peer.ID(nil), s.host.Network().Peers()...)
	if len(peers) < 2 {
		return peers
	}
	rotate := s.nextPeerOffset(len(peers))
	sort.SliceStable(peers, func(i, j int) bool {
		left, _, _ := s.peerScore(peers[i])
		right, _, _ := s.peerScore(peers[j])
		if left == right {
			return ((i + rotate) % len(peers)) < ((j + rotate) % len(peers))
		}
		return left > right
	})
	return peers
}

func (s *Service) peerScore(id peer.ID) (float64, bool, *peerStats) {
	publicIP := s.peerHasPublicIP(id)
	s.statsMu.Lock()
	stats := s.stats[id]
	var copyStats *peerStats
	if stats != nil {
		cp := *stats
		copyStats = &cp
	}
	s.statsMu.Unlock()

	score := 100.0
	if publicIP {
		score += 250.0
	}
	if copyStats == nil {
		return score, publicIP, nil
	}
	total := copyStats.successes + copyStats.failures
	if total > 0 {
		score += 200.0 * (float64(copyStats.successes) / float64(total))
		score -= 80.0 * float64(copyStats.failures)
	}
	if copyStats.avgLatency > 0 {
		latencyMS := float64(copyStats.avgLatency.Milliseconds())
		if latencyMS < 1 {
			latencyMS = 1
		}
		score += 300.0 / latencyMS
	}
	if !copyStats.lastSuccess.IsZero() && time.Since(copyStats.lastSuccess) < 5*time.Minute {
		score += 50
	}
	if !copyStats.lastFailure.IsZero() && time.Since(copyStats.lastFailure) < time.Minute {
		score -= 150
	}
	return score, publicIP, copyStats
}

func (s *Service) recordPeerResult(id peer.ID, latency time.Duration, err error) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	stats := s.stats[id]
	if stats == nil {
		stats = &peerStats{}
		s.stats[id] = stats
	}
	if err != nil {
		stats.failures++
		stats.lastFailure = time.Now()
		stats.lastError = err.Error()
		return
	}
	stats.successes++
	stats.lastSuccess = time.Now()
	stats.lastError = ""
	if stats.avgLatency == 0 {
		stats.avgLatency = latency
		return
	}
	stats.avgLatency = (stats.avgLatency*4 + latency) / 5
}

func (s *Service) handleRPCStream(st network.Stream) {
	defer st.Close()
	remotePeer := st.Conn().RemotePeer()
	if !s.acquireRPC(remotePeer) {
		_ = json.NewEncoder(st).Encode(jsonrpc.ErrorResponse(nil, -32005, "p2p rpc rate or concurrency limit exceeded"))
		_ = st.Reset()
		return
	}
	defer s.releaseRPC()
	_ = st.SetDeadline(time.Now().Add(rpcStreamTimeout))
	var req jsonrpc.Request
	if err := readLimitedJSON(st, s.cfg.MaxBodyBytes, &req); err != nil {
		_ = json.NewEncoder(st).Encode(jsonrpc.ErrorResponse(nil, -32700, err.Error()))
		return
	}
	if !proxy.P2PRequestAllowed(req) {
		_ = json.NewEncoder(st).Encode(jsonrpc.ErrorResponse(req.ID, -32601, "Method not allowed"))
		return
	}
	timeout := rpcTimeoutForMethod(req.Method)
	if req.Method == "scantxoutset" {
		if !s.scanGlobal.Allow() {
			_ = json.NewEncoder(st).Encode(jsonrpc.ErrorResponse(req.ID, -32005, "p2p UTXO scan rate limit exceeded"))
			return
		}
		select {
		case s.scanSlots <- struct{}{}:
			defer func() { <-s.scanSlots }()
		default:
			_ = json.NewEncoder(st).Encode(jsonrpc.ErrorResponse(req.ID, -32005, "p2p UTXO scan already running"))
			return
		}
	}
	_ = st.SetDeadline(time.Now().Add(timeout))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result, rpcErr := s.local.Call(ctx, req)
	if rpcErr != nil {
		_ = json.NewEncoder(st).Encode(jsonrpc.ErrorResponse(req.ID, rpcErr.Code, rpcErr.Message))
		return
	}
	_ = json.NewEncoder(st).Encode(jsonrpc.ResultResponse(req.ID, result))
}

// handleBrowserRPCStream is deliberately separate from the private overlay
// protocol. Browser transports cannot use pnet over WebTransport/WebRTC, so
// this public edge exposes only the narrow, parameter-validated P2P RPC
// profile. It never exposes pubsub, node tunnels, admin methods or credentials.
func (s *Service) handleBrowserRPCStream(st network.Stream) {
	defer st.Close()
	remotePeer := st.Conn().RemotePeer()
	if !s.acquireRPC(remotePeer) {
		_ = json.NewEncoder(st).Encode(jsonrpc.ErrorResponse(nil, -32005, "browser rpc rate or concurrency limit exceeded"))
		_ = st.Reset()
		return
	}
	defer s.releaseRPC()
	_ = st.SetDeadline(time.Now().Add(rpcStreamTimeout))
	var req jsonrpc.Request
	if err := readLimitedJSON(st, s.cfg.MaxBodyBytes, &req); err != nil {
		_ = json.NewEncoder(st).Encode(jsonrpc.ErrorResponse(nil, -32700, err.Error()))
		return
	}
	if len(req.Params) == 0 {
		req.Params = json.RawMessage("[]")
	}
	if !proxy.P2PRequestAllowed(req) {
		_ = json.NewEncoder(st).Encode(jsonrpc.ErrorResponse(req.ID, -32601, "Method not allowed"))
		return
	}
	timeout := rpcTimeoutForMethod(req.Method)
	if req.Method == "scantxoutset" {
		if !s.scanGlobal.Allow() {
			_ = json.NewEncoder(st).Encode(jsonrpc.ErrorResponse(req.ID, -32005, "browser UTXO scan rate limit exceeded"))
			return
		}
		select {
		case s.scanSlots <- struct{}{}:
			defer func() { <-s.scanSlots }()
		default:
			_ = json.NewEncoder(st).Encode(jsonrpc.ErrorResponse(req.ID, -32005, "browser UTXO scan already running"))
			return
		}
	}
	_ = st.SetDeadline(time.Now().Add(timeout + 6*time.Second))
	localTimeout := 3 * time.Second
	if req.Method == "scantxoutset" {
		localTimeout = 6 * time.Second
	}
	localCtx, localCancel := context.WithTimeout(context.Background(), localTimeout)
	result, rpcErr := s.local.Call(localCtx, req)
	localCancel()
	if rpcErr != nil {
		// The browser edge remains useful when this gateway's own node is down:
		// relay through the authenticated proxy overlay with a fresh timeout.
		// Reusing localCtx here would make the relay fail immediately whenever a
		// stalled local RPC consumed its entire deadline.
		relayCtx, relayCancel := context.WithTimeout(context.Background(), timeout)
		relayed, relayErr := s.CallAny(relayCtx, req)
		relayCancel()
		if relayErr == nil {
			_ = json.NewEncoder(st).Encode(jsonrpc.ResultResponse(req.ID, relayed))
			return
		}
		_ = json.NewEncoder(st).Encode(jsonrpc.ErrorResponse(req.ID, rpcErr.Code, rpcErr.Message))
		return
	}
	_ = json.NewEncoder(st).Encode(jsonrpc.ResultResponse(req.ID, result))
}

func rpcTimeoutForMethod(method string) time.Duration {
	if method == "scantxoutset" {
		return 18 * time.Second
	}
	return rpcStreamTimeout
}

func readLimitedJSON(r io.Reader, maxBytes int64, dst any) error {
	if maxBytes <= 0 {
		return errors.New("invalid json byte limit")
	}
	limited := &io.LimitedReader{R: r, N: maxBytes + 1}
	if err := json.NewDecoder(limited).Decode(dst); err != nil {
		return err
	}
	if limited.N <= 0 {
		return fmt.Errorf("json payload exceeds %d bytes", maxBytes)
	}
	return nil
}

func (s *Service) acquireRPC(id peer.ID) bool {
	s.rpcMu.Lock()
	if !s.rpcGlobal.Allow() {
		s.rpcMu.Unlock()
		return false
	}
	limiter := s.rpcLimits[id]
	if limiter == nil {
		limiter = rate.NewLimiter(rate.Limit(s.cfg.P2PRPCRateLimit), s.cfg.P2PRPCBurst)
		s.rpcLimits[id] = limiter
	}
	if len(s.rpcLimits) > 1024 {
		for known := range s.rpcLimits {
			if s.host.Network().Connectedness(known) == network.NotConnected {
				delete(s.rpcLimits, known)
			}
		}
	}
	for len(s.rpcLimits) > 4096 {
		for known := range s.rpcLimits {
			if known != id {
				delete(s.rpcLimits, known)
				break
			}
		}
	}
	allowed := limiter.Allow()
	s.rpcMu.Unlock()
	if !allowed {
		return false
	}
	select {
	case s.rpcSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Service) releaseRPC() {
	<-s.rpcSlots
}

func (s *Service) handleNodeP2PStream(st network.Stream) {
	defer st.Close()
	remotePeer := st.Conn().RemotePeer()
	if !s.acquireTunnel(remotePeer) {
		s.recordP2PFailure(remotePeer, errors.New("inbound tunnel limit exceeded"))
		_ = st.Reset()
		return
	}
	defer s.releaseTunnel(remotePeer)
	timeout := time.Duration(s.cfg.P2PHandshakeTimeout) * time.Second
	remoteVersion, err := readP2PVersion(st, s.cfg.NodeP2PMagic, timeout)
	if err != nil {
		s.recordP2PFailure(remotePeer, err)
		_ = st.Reset()
		return
	}
	conn, address, err := s.dialLocalNodeP2P(10 * time.Second)
	if err != nil {
		log.Printf("[node-p2p] local dial failed: %v", err)
		s.setLocalNodeP2PStatus(false, "")
		s.recordP2PFailure(remotePeer, err)
		_ = st.Reset()
		return
	}
	defer conn.Close()
	s.setLocalNodeP2PStatus(true, address)
	if err := writeP2PVersion(conn, remoteVersion, timeout); err != nil {
		s.recordP2PFailure(remotePeer, err)
		return
	}
	localVersion, err := readP2PVersion(conn, s.cfg.NodeP2PMagic, timeout)
	if err != nil {
		s.recordP2PFailure(remotePeer, err)
		return
	}
	if err := writeP2PVersion(st, localVersion, timeout); err != nil {
		s.recordP2PFailure(remotePeer, err)
		return
	}
	s.recordP2PSuccess(remotePeer, localVersion.protocolVersion, remoteVersion.protocolVersion)
	pipeBothLimited(context.Background(), st, conn, s.cfg.P2PMaxBytesPerSecond)
}

func (s *Service) connectCachedAndBootstrap(ctx context.Context) error {
	var errs []error
	byID := make(map[peer.ID]*peer.AddrInfo)
	var order []peer.ID
	for _, text := range s.bootstrapPeerList(ctx) {
		info, err := addrInfo(text)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if info.ID == s.host.ID() {
			continue
		}
		if existing := byID[info.ID]; existing != nil {
			existing.Addrs = appendUniqueMultiaddrs(existing.Addrs, info.Addrs...)
			continue
		}
		copyInfo := *info
		copyInfo.Addrs = appendUniqueMultiaddrs(nil, info.Addrs...)
		byID[info.ID] = &copyInfo
		order = append(order, info.ID)
	}
	for _, id := range order {
		info := byID[id]
		connectCtx, cancel := context.WithTimeout(ctx, bootstrapTimeout)
		err := s.host.Connect(connectCtx, *info)
		cancel()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		log.Printf("[p2p] connected to %s", info.ID)
	}
	return errors.Join(errs...)
}

func appendUniqueMultiaddrs(existing []ma.Multiaddr, additions ...ma.Multiaddr) []ma.Multiaddr {
	seen := make(map[string]struct{}, len(existing)+len(additions))
	for _, addr := range existing {
		seen[addr.String()] = struct{}{}
	}
	for _, addr := range additions {
		if _, ok := seen[addr.String()]; ok {
			continue
		}
		seen[addr.String()] = struct{}{}
		existing = append(existing, addr)
	}
	return existing
}

func (s *Service) bootstrapPeerList(ctx context.Context) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(items []string) {
		for _, item := range items {
			if len(out) >= maxBootstrapPeers {
				return
			}
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			out = append(out, item)
		}
	}
	add(loadPeerCache(s.cfg.PeerStorePath))
	add(s.cfg.BootstrapPeers)
	for _, url := range s.cfg.BootstrapURLs {
		peers, err := fetchBootstrapURL(ctx, url)
		if err != nil {
			log.Printf("[bootstrap] %s: %v", url, err)
			continue
		}
		add(peers)
	}
	return out
}

func (s *Service) pubsubDrain(ctx context.Context) {
	sub, err := s.topic.Subscribe()
	if err != nil {
		log.Printf("[pubsub] subscribe failed: %v", err)
		return
	}
	defer sub.Cancel()
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			return
		}
		if msg.ReceivedFrom == s.host.ID() {
			continue
		}
		var ann peerAnnounce
		if err := json.Unmarshal(msg.Data, &ann); err == nil && ann.Type == "peer_announce" {
			if origin := msg.GetFrom(); origin == "" || ann.PeerID != origin.String() {
				continue
			}
			s.handlePeerAnnounce(ctx, ann)
			continue
		}
		// Application messages are opaque signals. Do not log peer-controlled
		// payloads or one line per message, which would enable log amplification.
	}
}

func (s *Service) announceLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	s.publishAnnounce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.publishAnnounce(ctx)
		}
	}
}

func (s *Service) publishAnnounce(ctx context.Context) {
	available := s.checkLocalNodeP2P()
	ann := peerAnnounce{
		Type:             "peer_announce",
		NetworkID:        s.overlay.NetworkID,
		PeerID:           s.host.ID().String(),
		Time:             time.Now().Unix(),
		NodeP2PAvailable: &available,
	}
	for _, addr := range s.host.Addrs() {
		if len(ann.Addrs) >= maxAnnouncedAddrs {
			break
		}
		ann.Addrs = append(ann.Addrs, addr.Encapsulate(ma.StringCast("/p2p/"+s.host.ID().String())).String())
	}
	data, err := json.Marshal(ann)
	if err != nil {
		return
	}
	_ = s.topic.Publish(ctx, data)
}

func (s *Service) startNodeP2PProxy(ctx context.Context) {
	ln, err := net.Listen("tcp", s.cfg.P2PProxyListen)
	if err != nil {
		log.Printf("[node-p2p] listen %s failed: %v", s.cfg.P2PProxyListen, err)
		return
	}
	s.tcpLn = ln
	s.tunnelMu.Lock()
	s.p2pHealth.Listening = true
	s.p2pHealth.Listen = ln.Addr().String()
	s.tunnelMu.Unlock()
	defer func() {
		s.tunnelMu.Lock()
		s.p2pHealth.Listening = false
		s.tunnelMu.Unlock()
	}()
	log.Printf("[node-p2p] proxy listening on %s -> overlay %s", ln.Addr(), s.overlay.NodeP2PProtocol)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("[node-p2p] accept failed: %v", err)
			continue
		}
		go s.handleLocalNodeP2PConn(ctx, conn)
	}
}

func (s *Service) handleLocalNodeP2PConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	timeout := time.Duration(s.cfg.P2PHandshakeTimeout) * time.Second
	localVersion, err := readP2PVersion(conn, s.cfg.NodeP2PMagic, timeout)
	if err != nil {
		s.recordP2PFailure("", err)
		log.Printf("[node-p2p] rejected local connection: %v", err)
		return
	}
	peerID, err := s.choosePeer()
	if err != nil {
		log.Printf("[node-p2p] no overlay peer: %v", err)
		s.recordP2PFailure("", err)
		return
	}
	if !s.acquireTunnel(peerID) {
		err := errors.New("outbound tunnel limit exceeded")
		s.recordP2PFailure(peerID, err)
		log.Printf("[node-p2p] %v for %s", err, peerID)
		return
	}
	defer s.releaseTunnel(peerID)
	streamCtx, cancel := context.WithTimeout(ctx, nodeStreamTimeout)
	st, err := s.host.NewStream(streamCtx, peerID, s.overlay.NodeP2PProtocol)
	cancel()
	if err != nil {
		log.Printf("[node-p2p] open stream to %s failed: %v", peerID, err)
		s.recordP2PFailure(peerID, err)
		return
	}
	defer st.Close()
	if err := writeP2PVersion(st, localVersion, timeout); err != nil {
		s.recordP2PFailure(peerID, err)
		return
	}
	remoteVersion, err := readP2PVersion(st, s.cfg.NodeP2PMagic, timeout)
	if err != nil {
		s.recordP2PFailure(peerID, err)
		return
	}
	if err := writeP2PVersion(conn, remoteVersion, timeout); err != nil {
		s.recordP2PFailure(peerID, err)
		return
	}
	s.recordP2PSuccess(peerID, localVersion.protocolVersion, remoteVersion.protocolVersion)
	pipeBothLimited(ctx, conn, st, s.cfg.P2PMaxBytesPerSecond)
}

func (s *Service) acquireTunnel(id peer.ID) bool {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	total := 0
	for _, count := range s.tunnels {
		total += count
	}
	if total >= s.cfg.P2PMaxTunnels || s.tunnels[id] >= s.cfg.P2PMaxTunnelsPerPeer {
		return false
	}
	s.tunnels[id]++
	return true
}

func (s *Service) releaseTunnel(id peer.ID) {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	if s.tunnels[id] <= 1 {
		delete(s.tunnels, id)
		return
	}
	s.tunnels[id]--
}

func (s *Service) recordP2PSuccess(id peer.ID, localVersion, remoteVersion int32) {
	s.capMu.Lock()
	s.nodePeers[id] = true
	s.capMu.Unlock()
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	s.p2pHealth.SuccessfulHandshakes++
	s.p2pHealth.LastHandshakeUnix = time.Now().Unix()
	s.p2pHealth.LastRemotePeer = id.String()
	s.p2pHealth.LastLocalVersion = localVersion
	s.p2pHealth.LastRemoteVersion = remoteVersion
	s.p2pHealth.LocalNodeReachable = true
	s.p2pHealth.LastLocalCheckUnix = time.Now().Unix()
	s.p2pHealth.LastError = ""
	log.Printf("[node-p2p] protocol handshake ok with %s (local=%d remote=%d)", id, localVersion, remoteVersion)
}

func (s *Service) recordP2PFailure(id peer.ID, err error) {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	s.p2pHealth.FailedHandshakes++
	s.p2pHealth.LastRemotePeer = id.String()
	s.p2pHealth.LastError = err.Error()
}

func (s *Service) choosePeer() (peer.ID, error) {
	peers := s.rankedPeers()
	if len(peers) == 0 {
		return "", errors.New("no connected overlay peers")
	}
	s.capMu.RLock()
	defer s.capMu.RUnlock()
	for _, id := range peers {
		if s.nodePeers[id] {
			return id, nil
		}
	}
	for _, id := range peers {
		if _, known := s.nodePeers[id]; !known {
			return id, nil
		}
	}
	return "", errors.New("connected overlay peers report no reachable ZHCASH P2P node")
}

func (s *Service) checkLocalNodeP2P() bool {
	s.tunnelMu.Lock()
	if checked := s.p2pHealth.LastLocalCheckUnix; checked > 0 && time.Since(time.Unix(checked, 0)) < time.Minute {
		available := s.p2pHealth.LocalNodeReachable
		s.tunnelMu.Unlock()
		return available
	}
	s.tunnelMu.Unlock()

	conn, address, err := s.dialLocalNodeP2P(750 * time.Millisecond)
	if err == nil {
		_ = conn.Close()
	}
	s.setLocalNodeP2PStatus(err == nil, address)
	return err == nil
}

func (s *Service) dialLocalNodeP2P(timeout time.Duration) (net.Conn, string, error) {
	ports := []int{s.cfg.NodeP2PPort, 38100, 8003, 3888}
	seen := make(map[int]struct{}, len(ports))
	var lastErr error
	for _, port := range ports {
		if port <= 0 || port > 65535 {
			continue
		}
		if _, ok := seen[port]; ok {
			continue
		}
		seen[port] = struct{}{}
		address := net.JoinHostPort(s.cfg.NodeP2PHost, fmt.Sprint(port))
		conn, err := net.DialTimeout("tcp", address, timeout)
		if err == nil {
			return conn, address, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no valid local node P2P ports configured")
	}
	return nil, "", lastErr
}

func (s *Service) setLocalNodeP2PStatus(available bool, address string) {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	s.p2pHealth.LocalNodeReachable = available
	s.p2pHealth.LocalNodeAddress = address
	s.p2pHealth.LastLocalCheckUnix = time.Now().Unix()
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

func (s *Service) peerHasPublicIP(id peer.ID) bool {
	for _, addr := range s.host.Peerstore().Addrs(id) {
		if multiaddrHasPublicIP(addr.String()) {
			return true
		}
	}
	for _, conn := range s.host.Network().ConnsToPeer(id) {
		if multiaddrHasPublicIP(conn.RemoteMultiaddr().String()) {
			return true
		}
	}
	return false
}

func multiaddrHasPublicIP(addr string) bool {
	parts := strings.Split(addr, "/")
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] != "ip4" && parts[i] != "ip6" {
			continue
		}
		ip := net.ParseIP(parts[i+1])
		if ip == nil {
			continue
		}
		if isPublicIP(ip) {
			return true
		}
	}
	return false
}

func isPublicIP(ip net.IP) bool {
	return !ip.IsLoopback() &&
		!ip.IsPrivate() &&
		!ip.IsUnspecified() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast()
}

func (s *Service) handlePeerAnnounce(ctx context.Context, ann peerAnnounce) {
	if ann.NetworkID != s.overlay.NetworkID || ann.PeerID == s.host.ID().String() {
		return
	}
	if delta := time.Since(time.Unix(ann.Time, 0)); delta > 5*time.Minute || delta < -5*time.Minute {
		return
	}
	id, err := peer.Decode(ann.PeerID)
	if err != nil {
		return
	}
	if ann.NodeP2PAvailable != nil {
		s.capMu.Lock()
		s.nodePeers[id] = *ann.NodeP2PAvailable
		s.capMu.Unlock()
	}
	if s.host.Network().Connectedness(id) == network.Connected {
		return
	}
	info := peer.AddrInfo{ID: id}
	for index, text := range ann.Addrs {
		if index >= maxAnnouncedAddrs {
			break
		}
		if !safeAnnouncedAddress(text) {
			continue
		}
		addr, err := ma.NewMultiaddr(text)
		if err != nil {
			continue
		}
		parsed, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil || parsed.ID != id {
			continue
		}
		info.Addrs = append(info.Addrs, parsed.Addrs...)
	}
	if len(info.Addrs) == 0 {
		return
	}
	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := s.host.Connect(connectCtx, info); err == nil {
		log.Printf("[overlay] connected announced peer %s", ann.PeerID)
	}
}

func safeAnnouncedAddress(text string) bool {
	if strings.Contains(text, "/p2p-circuit/") || strings.HasSuffix(text, "/p2p-circuit") {
		return true
	}
	return multiaddrHasPublicIP(text)
}

func (s *Service) peerCacheLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			savePeerCache(s.cfg.PeerStorePath, s.host)
			return
		case <-ticker.C:
			savePeerCache(s.cfg.PeerStorePath, s.host)
		}
	}
}

func listenAddrs(cfg config.Config) []string {
	port := cfg.P2PPort
	if port == 0 {
		port = 0
	}
	addrs := []string{
		fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port),
	}
	if cfg.EnableIPv6 {
		addrs = append(addrs, fmt.Sprintf("/ip6/::/tcp/%d", port))
	}
	if cfg.EnableQUIC {
		addrs = append(addrs, fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", port))
		if cfg.EnableIPv6 {
			addrs = append(addrs, fmt.Sprintf("/ip6/::/udp/%d/quic-v1", port))
		}
	}
	return addrs
}

func browserRPCProtocol(cfg config.Config) protocol.ID {
	return protocol.ID(browserRPCBase + "/" + sanitizeNetworkID(cfg.OverlayNetworkID))
}

func browserListenAddrs(cfg config.Config) []string {
	var addrs []string
	if cfg.BrowserWebTransport {
		port := cfg.BrowserWebTransportPort
		if port <= 0 && cfg.P2PPort > 0 {
			port = cfg.P2PPort + 2
		}
		addrs = append(addrs, fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1/webtransport", port))
		if cfg.EnableIPv6 {
			addrs = append(addrs, fmt.Sprintf("/ip6/::/udp/%d/quic-v1/webtransport", port))
		}
	}
	if cfg.BrowserWebRTC {
		port := cfg.BrowserWebRTCPort
		if port <= 0 && cfg.P2PPort > 0 {
			port = cfg.P2PPort + 3
		}
		addrs = append(addrs, fmt.Sprintf("/ip4/0.0.0.0/udp/%d/webrtc-direct", port))
		if cfg.EnableIPv6 {
			addrs = append(addrs, fmt.Sprintf("/ip6/::/udp/%d/webrtc-direct", port))
		}
	}
	return addrs
}

func newBrowserHost(cfg config.Config, identity crypto.PrivKey) (host.Host, error) {
	addrs := browserListenAddrs(cfg)
	if len(addrs) == 0 {
		return nil, errors.New("no browser transport listen addresses configured")
	}
	return libp2p.New(
		libp2p.Identity(identity),
		libp2p.ListenAddrStrings(addrs...),
		libp2p.ForceReachabilityPublic(),
	)
}

func (s *Service) browserMultiaddrs() []string {
	if s.browserHost == nil {
		return nil
	}
	suffix := ma.StringCast("/p2p/" + s.browserHost.ID().String())
	addrs := make([]string, 0, len(s.browserHost.Addrs()))
	for _, addr := range s.browserHost.Addrs() {
		addrs = append(addrs, addr.Encapsulate(suffix).String())
	}
	sort.Strings(addrs)
	return addrs
}

func addrInfo(text string) (*peer.AddrInfo, error) {
	addr, err := ma.NewMultiaddr(text)
	if err != nil {
		return nil, err
	}
	return peer.AddrInfoFromP2pAddr(addr)
}

func makeOverlaySpec(cfg config.Config) overlaySpec {
	id := sanitizeNetworkID(cfg.OverlayNetworkID)
	return overlaySpec{
		NetworkID:       id,
		Rendezvous:      "zhcash-rpc-proxy/" + id,
		Topic:           "zhcash-rpc-tx/" + id,
		RPCProtocol:     protocol.ID(rpcProtocolBase + "/" + id),
		NodeP2PProtocol: protocol.ID(nodeP2PBase + "/" + id),
		Private:         cfg.OverlayPrivate,
	}
}

func sanitizeNetworkID(id string) string {
	id = strings.TrimSpace(strings.ToLower(id))
	if id == "" {
		return "zhcash-proxy-main"
	}
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "zhcash-proxy-main"
	}
	return b.String()
}

func overlayPSK(cfg config.Config) (pnet.PSK, error) {
	if cfg.OverlayPSK != "" {
		if raw, err := hex.DecodeString(strings.TrimSpace(cfg.OverlayPSK)); err == nil && len(raw) == 32 {
			return pnet.PSK(raw), nil
		}
		if raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cfg.OverlayPSK)); err == nil && len(raw) == 32 {
			return pnet.PSK(raw), nil
		}
		return nil, errors.New("overlay_psk must be 32 bytes encoded as hex or base64")
	}
	sum := sha256.Sum256([]byte("zhp2pproxy-overlay:" + sanitizeNetworkID(cfg.OverlayNetworkID)))
	return pnet.PSK(sum[:]), nil
}

func parseStaticRelays(addrs []string) []peer.AddrInfo {
	var relays []peer.AddrInfo
	for _, text := range addrs {
		info, err := addrInfo(text)
		if err != nil || info == nil || info.ID == "" {
			continue
		}
		relays = append(relays, *info)
	}
	return relays
}

func loadOrCreateIdentity(path string) (crypto.PrivKey, error) {
	if data, err := securefs.ReadFile(path); err == nil {
		raw, err := base64.StdEncoding.DecodeString(string(data))
		if err == nil {
			priv, err := crypto.UnmarshalPrivateKey(raw)
			if err == nil {
				return priv, nil
			}
		}
		backup := fmt.Sprintf("%s.bad.%d", path, time.Now().Unix())
		_ = securefs.Rename(path, backup)
		log.Printf("[p2p] replaced unreadable identity key; old file moved to %s", backup)
	}
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		return nil, err
	}
	raw, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	return priv, securefs.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(raw)), 0600)
}

func loadPeerCache(path string) []string {
	data, err := securefs.ReadFile(path)
	if err != nil {
		return nil
	}
	var peers []string
	if err := json.Unmarshal(data, &peers); err != nil {
		return nil
	}
	filtered := peers[:0]
	for _, address := range peers {
		if safeAnnouncedAddress(address) {
			filtered = append(filtered, address)
		}
	}
	return filtered
}

func savePeerCache(path string, h host.Host) {
	var peers []string
	for _, id := range h.Network().Peers() {
		for _, addr := range h.Peerstore().Addrs(id) {
			full := addr.Encapsulate(ma.StringCast("/p2p/" + id.String())).String()
			if safeAnnouncedAddress(full) {
				peers = append(peers, full)
			}
		}
	}
	data, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		return
	}
	_ = securefs.WriteFile(path, append(data, '\n'), 0600)
}

func fetchBootstrapURL(ctx context.Context, url string) ([]string, error) {
	parsed, err := neturlParseHTTPS(url)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if req.URL.Scheme != "https" {
				return errors.New("bootstrap redirect must use https")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var arr []string
	if err := json.Unmarshal(body, &arr); err == nil {
		return limitStrings(arr, maxBootstrapPeers), nil
	}
	var doc bootstrapDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	return limitStrings(append(doc.Peers, doc.BootstrapPeers...), maxBootstrapPeers), nil
}

func limitStrings(values []string, maximum int) []string {
	if len(values) <= maximum {
		return values
	}
	return values[:maximum]
}

func neturlParseHTTPS(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("bootstrap URL must be an https URL without credentials")
	}
	return parsed, nil
}
