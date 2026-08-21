package p2p

import (
	"bufio"
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
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	discoveryutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	ma "github.com/multiformats/go-multiaddr"

	"zhp2pproxy/internal/config"
	"zhp2pproxy/internal/node"
	"zhp2pproxy/internal/proxy"
	"zhp2pproxy/pkg/jsonrpc"
)

const (
	rpcProtocolBase   = "/zhcash/rpc/1.0.0"
	nodeP2PBase       = "/zhcash/node-p2p/1.0.0"
	rpcStreamTimeout  = 5 * time.Second
	nodeStreamTimeout = 20 * time.Second
)

type Service struct {
	cfg       config.Config
	host      host.Host
	dht       *dht.IpfsDHT
	pubsub    *pubsub.PubSub
	topic     *pubsub.Topic
	local     *node.Client
	overlay   overlaySpec
	tcpLn     net.Listener
	rpcPeerMu sync.Mutex
	rpcPeerIx int
	statsMu   sync.Mutex
	stats     map[peer.ID]*peerStats
	closeOnce sync.Once
}

type Info struct {
	PeerID       string   `json:"peer_id"`
	NetworkID    string   `json:"network_id"`
	Listen       []string `json:"listen,omitempty"`
	Peers        []string `json:"peers,omitempty"`
	Private      bool     `json:"private"`
	OverlayMode  string   `json:"overlay_mode"`
	ForceRelay   bool     `json:"force_relay"`
	Relay        bool     `json:"relay"`
	IPv6         bool     `json:"ipv6"`
	QUIC         bool     `json:"quic"`
	NATPortMap   bool     `json:"nat_port_map"`
	HolePunching bool     `json:"hole_punching"`
	Rendezvous   string   `json:"rendezvous"`
	Topic        string   `json:"topic"`
	RPCProtocol  string   `json:"rpc_protocol"`
	NodeP2P      string   `json:"node_p2p_protocol"`
	P2PProxy     string   `json:"p2p_proxy_listen"`
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
	Type      string   `json:"type"`
	NetworkID string   `json:"network_id"`
	PeerID    string   `json:"peer_id"`
	Addrs     []string `json:"addrs"`
	Time      int64    `json:"time"`
}

type bootstrapDocument struct {
	Peers          []string `json:"peers"`
	BootstrapPeers []string `json:"bootstrap_peers"`
}

type AdminState struct {
	NetworkID       string      `json:"network_id"`
	Private         bool        `json:"private"`
	PeerID          string      `json:"peer_id"`
	OverlayMode     string      `json:"overlay_mode"`
	ForceRelay      bool        `json:"force_relay"`
	Rendezvous      string      `json:"rendezvous"`
	Topic           string      `json:"topic"`
	RPCProtocol     string      `json:"rpc_protocol"`
	NodeP2PProtocol string      `json:"node_p2p_protocol"`
	P2PProxyListen  string      `json:"p2p_proxy_listen"`
	ListenAddrs     []string    `json:"listen_addrs"`
	BootstrapPeers  []string    `json:"bootstrap_peers"`
	BootstrapURLs   []string    `json:"bootstrap_urls"`
	ConnectedPeers  []AdminPeer `json:"connected_peers"`
	KnownPeers      []AdminPeer `json:"known_peers"`
}

type AdminPeer struct {
	ID              string   `json:"id"`
	Connectedness   string   `json:"connectedness"`
	Score           float64  `json:"score"`
	PublicIP        bool     `json:"public_ip"`
	Successes       uint64   `json:"successes"`
	Failures        uint64   `json:"failures"`
	AvgLatencyMS    int64    `json:"avg_latency_ms"`
	LastError       string   `json:"last_error,omitempty"`
	LastSuccessUnix int64    `json:"last_success_unix,omitempty"`
	LastFailureUnix int64    `json:"last_failure_unix,omitempty"`
	Addrs           []string `json:"addrs,omitempty"`
	Conns           []string `json:"conns,omitempty"`
}

type peerStats struct {
	successes   uint64
	failures    uint64
	avgLatency  time.Duration
	lastError   string
	lastSuccess time.Time
	lastFailure time.Time
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

func New(ctx context.Context, cfg config.Config, local *node.Client) (*Service, error) {
	priv, err := loadOrCreateIdentity(cfg.IdentityKeyPath)
	if err != nil {
		return nil, err
	}
	overlay := makeOverlaySpec(cfg)
	addrs := listenAddrs(cfg)
	opts := []libp2p.Option{
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(addrs...),
		libp2p.EnableRelay(),
	}
	if cfg.OverlayPrivate {
		psk, err := overlayPSK(cfg)
		if err != nil {
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
		opts = append(opts, libp2p.EnableRelayService())
	}
	staticRelays := parseStaticRelays(cfg.BootstrapPeers)
	if cfg.ForceRelay || cfg.OverlayMode == "relay-only" || cfg.OverlayMode == "relay-first" {
		opts = append(opts, libp2p.ForceReachabilityPrivate())
		if len(staticRelays) > 0 {
			opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(staticRelays))
		}
	}
	h, err := libp2p.New(opts...)
	if err != nil {
		return nil, err
	}
	kad, err := dht.New(ctx, h, dht.Mode(dht.ModeAuto))
	if err != nil {
		_ = h.Close()
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
	s := &Service{cfg: cfg, host: h, dht: kad, pubsub: ps, topic: topic, local: local, overlay: overlay, stats: make(map[peer.ID]*peerStats)}
	h.SetStreamHandler(overlay.RPCProtocol, s.handleRPCStream)
	h.SetStreamHandler(overlay.NodeP2PProtocol, s.handleNodeP2PStream)
	if err := s.connectCachedAndBootstrap(ctx); err != nil {
		log.Printf("[p2p] bootstrap warning: %v", err)
	}
	if err := kad.Bootstrap(ctx); err != nil {
		log.Printf("[p2p] dht bootstrap warning: %v", err)
	}
	go s.discoveryLoop(ctx)
	go s.pubsubDrain(ctx)
	go s.announceLoop(ctx)
	go s.peerCacheLoop(ctx)
	if cfg.NativeP2PProxy {
		go s.startNodeP2PProxy(ctx)
	}
	return s, nil
}

func (s *Service) Close() error {
	var err error
	s.closeOnce.Do(func() {
		if s.topic != nil {
			_ = s.topic.Close()
		}
		if s.dht != nil {
			_ = s.dht.Close()
		}
		if s.host != nil {
			err = s.host.Close()
		}
		if s.tcpLn != nil {
			_ = s.tcpLn.Close()
		}
	})
	return err
}

func (s *Service) Info() any {
	info := Info{
		PeerID:       s.host.ID().String(),
		NetworkID:    s.overlay.NetworkID,
		Private:      s.overlay.Private,
		OverlayMode:  s.cfg.OverlayMode,
		ForceRelay:   s.cfg.ForceRelay,
		Relay:        s.cfg.RelayEnabled,
		IPv6:         s.cfg.EnableIPv6,
		QUIC:         s.cfg.EnableQUIC,
		NATPortMap:   s.cfg.EnableNATPortMap,
		HolePunching: s.cfg.EnableHolePunching,
		Rendezvous:   s.overlay.Rendezvous,
		Topic:        s.overlay.Topic,
		RPCProtocol:  string(s.overlay.RPCProtocol),
		NodeP2P:      string(s.overlay.NodeP2PProtocol),
		P2PProxy:     s.cfg.P2PProxyListen,
	}
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
		NetworkID:       s.overlay.NetworkID,
		Private:         s.overlay.Private,
		PeerID:          s.host.ID().String(),
		OverlayMode:     s.cfg.OverlayMode,
		ForceRelay:      s.cfg.ForceRelay,
		Rendezvous:      s.overlay.Rendezvous,
		Topic:           s.overlay.Topic,
		RPCProtocol:     string(s.overlay.RPCProtocol),
		NodeP2PProtocol: string(s.overlay.NodeP2PProtocol),
		P2PProxyListen:  s.cfg.P2PProxyListen,
		BootstrapURLs:   append([]string(nil), s.cfg.BootstrapURLs...),
		BootstrapPeers:  append([]string(nil), s.cfg.BootstrapPeers...),
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
	var last error
	for _, id := range peers {
		started := time.Now()
		result, err := s.callPeer(ctx, id, req)
		if err == nil {
			s.recordPeerResult(id, time.Since(started), nil)
			return result, nil
		}
		s.recordPeerResult(id, time.Since(started), err)
		last = err
	}
	if last == nil {
		last = errors.New("no usable p2p peers")
	}
	return nil, last
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
	ctx, cancel := context.WithTimeout(ctx, rpcStreamTimeout)
	defer cancel()
	st, err := s.host.NewStream(ctx, id, s.overlay.RPCProtocol)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	_ = st.SetDeadline(time.Now().Add(rpcStreamTimeout))
	if err := json.NewEncoder(st).Encode(req); err != nil {
		_ = st.Reset()
		return nil, err
	}
	var resp jsonrpc.Response
	if err := json.NewDecoder(st).Decode(&resp); err != nil {
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
	_ = st.SetDeadline(time.Now().Add(rpcStreamTimeout))
	var req jsonrpc.Request
	if err := json.NewDecoder(bufio.NewReader(st)).Decode(&req); err != nil {
		_ = json.NewEncoder(st).Encode(jsonrpc.ErrorResponse(nil, -32700, err.Error()))
		return
	}
	if !proxy.MethodAllowed(req.Method) {
		_ = json.NewEncoder(st).Encode(jsonrpc.ErrorResponse(req.ID, -32601, "Method not allowed"))
		return
	}
	result, rpcErr := s.local.Call(context.Background(), req)
	if rpcErr != nil {
		_ = json.NewEncoder(st).Encode(jsonrpc.ErrorResponse(req.ID, rpcErr.Code, rpcErr.Message))
		return
	}
	_ = json.NewEncoder(st).Encode(jsonrpc.ResultResponse(req.ID, result))
}

func (s *Service) handleNodeP2PStream(st network.Stream) {
	defer st.Close()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", s.cfg.NodeP2PHost, s.cfg.NodeP2PPort), 10*time.Second)
	if err != nil {
		log.Printf("[node-p2p] local dial failed: %v", err)
		_ = st.Reset()
		return
	}
	defer conn.Close()
	pipeBoth(st, conn)
}

func (s *Service) connectCachedAndBootstrap(ctx context.Context) error {
	var errs []error
	for _, text := range s.bootstrapPeerList(ctx) {
		info, err := addrInfo(text)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if info.ID == s.host.ID() {
			continue
		}
		if err := s.host.Connect(ctx, *info); err != nil {
			errs = append(errs, err)
			continue
		}
		log.Printf("[p2p] connected to %s", info.ID)
	}
	return errors.Join(errs...)
}

func (s *Service) bootstrapPeerList(ctx context.Context) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(items []string) {
		for _, item := range items {
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

func (s *Service) discoveryLoop(ctx context.Context) {
	rd := routing.NewRoutingDiscovery(s.dht)
	discoveryutil.Advertise(ctx, rd, s.overlay.Rendezvous)
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		peers, err := rd.FindPeers(ctx, s.overlay.Rendezvous)
		if err == nil {
			for p := range peers {
				if p.ID == "" || p.ID == s.host.ID() {
					continue
				}
				connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				if err := s.host.Connect(connectCtx, p); err == nil {
					log.Printf("[p2p] discovered %s", p.ID)
				}
				cancel()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
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
			s.handlePeerAnnounce(ctx, ann)
			continue
		}
		log.Printf("[pubsub] received %d bytes from %s", len(msg.Data), msg.ReceivedFrom)
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
	ann := peerAnnounce{
		Type:      "peer_announce",
		NetworkID: s.overlay.NetworkID,
		PeerID:    s.host.ID().String(),
		Time:      time.Now().Unix(),
	}
	for _, addr := range s.host.Addrs() {
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
	log.Printf("[node-p2p] proxy listening on %s -> overlay %s", ln.Addr(), s.overlay.NodeP2PProtocol)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
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
	peerID, err := s.choosePeer()
	if err != nil {
		log.Printf("[node-p2p] no overlay peer: %v", err)
		return
	}
	streamCtx, cancel := context.WithTimeout(ctx, nodeStreamTimeout)
	st, err := s.host.NewStream(streamCtx, peerID, s.overlay.NodeP2PProtocol)
	cancel()
	if err != nil {
		log.Printf("[node-p2p] open stream to %s failed: %v", peerID, err)
		return
	}
	defer st.Close()
	pipeBoth(conn, st)
}

func (s *Service) choosePeer() (peer.ID, error) {
	peers := s.rankedPeers()
	if len(peers) == 0 {
		return "", errors.New("no connected overlay peers")
	}
	return peers[0], nil
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
	id, err := peer.Decode(ann.PeerID)
	if err != nil {
		return
	}
	info := peer.AddrInfo{ID: id}
	for _, text := range ann.Addrs {
		addr, err := ma.NewMultiaddr(text)
		if err != nil {
			continue
		}
		parsed, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil {
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
	if data, err := os.ReadFile(path); err == nil {
		raw, err := base64.StdEncoding.DecodeString(string(data))
		if err == nil {
			priv, err := crypto.UnmarshalPrivateKey(raw)
			if err == nil {
				return priv, nil
			}
		}
		backup := fmt.Sprintf("%s.bad.%d", path, time.Now().Unix())
		_ = os.Rename(path, backup)
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
	return priv, os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(raw)), 0600)
}

func loadPeerCache(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var peers []string
	if err := json.Unmarshal(data, &peers); err != nil {
		return nil
	}
	return peers
}

func savePeerCache(path string, h host.Host) {
	var peers []string
	for _, id := range h.Network().Peers() {
		for _, addr := range h.Peerstore().Addrs(id) {
			peers = append(peers, addr.Encapsulate(ma.StringCast("/p2p/"+id.String())).String())
		}
	}
	data, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, append(data, '\n'), 0600)
}

func fetchBootstrapURL(ctx context.Context, url string) ([]string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
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
		return arr, nil
	}
	var doc bootstrapDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	return append(doc.Peers, doc.BootstrapPeers...), nil
}

func pipeBoth(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(a, b)
		_ = a.Close()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(b, a)
		_ = b.Close()
		done <- struct{}{}
	}()
	<-done
}
