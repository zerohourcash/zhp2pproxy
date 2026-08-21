package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"zhp2pproxy/internal/config"
	"zhp2pproxy/internal/node"
	"zhp2pproxy/pkg/jsonrpc"
)

type P2PRPC interface {
	CallAny(ctx context.Context, req jsonrpc.Request) (json.RawMessage, error)
	CallPeer(ctx context.Context, peerID string, req jsonrpc.Request) (json.RawMessage, error)
	Info() any
	AdminState() any
	ConnectPeer(ctx context.Context, addr string) error
	Publish(ctx context.Context, data []byte) error
	PeerID() string
	NetworkID() string
	ConnectedPeerCount() int
}

type Handler struct {
	cfg     config.Config
	local   *node.Client
	p2p     P2PRPC
	metrics *Metrics
}

func NewHandler(cfg config.Config, local *node.Client, p2p P2PRPC, metrics *Metrics) *Handler {
	return &Handler{cfg: cfg, local: local, p2p: p2p, metrics: metrics}
}

func (h *Handler) RPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes))
	if err != nil {
		inc(&h.metrics.BadRequests)
		writeJSON(w, jsonrpc.ErrorResponse(nil, -32700, err.Error()))
		return
	}
	trimmed := firstNonSpace(body)
	if trimmed == '[' {
		h.handleBatch(w, r.Context(), body)
		return
	}
	var req jsonrpc.Request
	if err := json.Unmarshal(body, &req); err != nil {
		inc(&h.metrics.BadRequests)
		writeJSON(w, jsonrpc.ErrorResponse(nil, -32700, err.Error()))
		return
	}
	writeJSON(w, h.handleOne(r.Context(), req))
}

func (h *Handler) handleBatch(w http.ResponseWriter, ctx context.Context, body []byte) {
	var reqs []jsonrpc.Request
	if err := json.Unmarshal(body, &reqs); err != nil {
		inc(&h.metrics.BadRequests)
		writeJSON(w, jsonrpc.ErrorResponse(nil, -32700, err.Error()))
		return
	}
	if len(reqs) == 0 || len(reqs) > 100 {
		inc(&h.metrics.BadRequests)
		writeJSON(w, jsonrpc.ErrorResponse(nil, -32600, "batch size must be 1..100"))
		return
	}
	resps := make([]jsonrpc.Response, len(reqs))
	for i := range reqs {
		resps[i] = h.handleOne(ctx, reqs[i])
	}
	writeJSON(w, resps)
}

func (h *Handler) handleOne(ctx context.Context, req jsonrpc.Request) jsonrpc.Response {
	inc(&h.metrics.RequestsTotal)
	if req.Method == "" {
		inc(&h.metrics.BadRequests)
		return jsonrpc.ErrorResponse(req.ID, -32600, "missing method")
	}
	if !MethodAllowed(req.Method) {
		inc(&h.metrics.MethodDenied)
		return jsonrpc.ErrorResponse(req.ID, -32601, "Method not allowed")
	}
	if len(req.Params) == 0 {
		req.Params = json.RawMessage("[]")
	}
	if h.cfg.P2PLoadBalance && CanTryP2PFirst(req.Method) && h.p2p != nil && !h.cfg.LocalRPCPreferDirect {
		if result, err := h.callP2P(ctx, req); err == nil {
			return jsonrpc.ResultResponse(req.ID, result)
		}
	}
	result, rpcErr := h.local.Call(ctx, req)
	if rpcErr == nil {
		inc(&h.metrics.LocalSuccesses)
		return jsonrpc.ResultResponse(req.ID, result)
	}
	inc(&h.metrics.LocalFailures)
	if h.p2p != nil && req.Method != "scantxoutset" {
		if result, err := h.callP2P(ctx, req); err == nil {
			return jsonrpc.ResultResponse(req.ID, result)
		}
	}
	return jsonrpc.ErrorResponse(req.ID, rpcErr.Code, rpcErr.Message)
}

func (h *Handler) callP2P(ctx context.Context, req jsonrpc.Request) (json.RawMessage, error) {
	if h.p2p == nil {
		return nil, errors.New("p2p unavailable")
	}
	inc(&h.metrics.P2PAttempts)
	result, err := h.p2p.CallAny(ctx, req)
	if err != nil {
		inc(&h.metrics.P2PFailures)
		return nil, err
	}
	inc(&h.metrics.P2PSuccesses)
	return result, nil
}

func (h *Handler) Info(w http.ResponseWriter, _ *http.Request) {
	info := map[string]any{
		"metrics": h.metrics.Snapshot(),
		"p2p":     nil,
	}
	if h.p2p != nil {
		info["p2p"] = h.p2p.Info()
	}
	writeJSON(w, info)
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	peerID := ""
	networkID := ""
	connectedPeers := 0
	if h.p2p != nil {
		peerID = h.p2p.PeerID()
		networkID = h.p2p.NetworkID()
		connectedPeers = h.p2p.ConnectedPeerCount()
	}

	localConnected := false
	var blockHeight any
	localError := ""
	ctx, cancel := context.WithTimeout(r.Context(), 1500*time.Millisecond)
	defer cancel()
	result, rpcErr := h.local.Call(ctx, jsonrpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "getblockcount",
		Params:  json.RawMessage("[]"),
	})
	if rpcErr == nil {
		localConnected = true
		if err := json.Unmarshal(result, &blockHeight); err != nil {
			blockHeight = string(result)
		}
	} else {
		localError = rpcErr.Message
	}

	p2pConnected := peerID != "" && connectedPeers > 0
	writeJSON(w, map[string]any{
		"ok": localConnected || p2pConnected,
		"local_node": map[string]any{
			"connected":    localConnected,
			"block_height": blockHeight,
			"error":        localError,
			"optional":     true,
		},
		"p2p_network": map[string]any{
			"connected":       p2pConnected,
			"connected_peers": connectedPeers,
			"network_id":      networkID,
			"peer_id":         peerID,
		},
	})
}

func (h *Handler) Publish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if h.p2p == nil {
		http.Error(w, "p2p unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := h.p2p.Publish(r.Context(), body); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (h *Handler) AdminStatus(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeAdmin(w, r) {
		return
	}
	if h.p2p == nil {
		writeJSON(w, map[string]any{"p2p": nil, "metrics": h.metrics.Snapshot()})
		return
	}
	writeJSON(w, map[string]any{
		"metrics": h.metrics.Snapshot(),
		"overlay": h.p2p.AdminState(),
	})
}

func (h *Handler) AdminConnect(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var req struct {
		Addr string `json:"addr"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Addr == "" {
		http.Error(w, "missing addr", http.StatusBadRequest)
		return
	}
	if h.p2p == nil {
		http.Error(w, "p2p unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := h.p2p.ConnectPeer(r.Context(), req.Addr); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (h *Handler) AdminProbe(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var probe struct {
		PeerID string          `json:"peer_id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes)).Decode(&probe); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if probe.PeerID == "" {
		http.Error(w, "missing peer_id", http.StatusBadRequest)
		return
	}
	if probe.Method == "" {
		probe.Method = "getblockcount"
	}
	if !MethodAllowed(probe.Method) {
		http.Error(w, "method not allowed", http.StatusBadRequest)
		return
	}
	if len(probe.Params) == 0 {
		probe.Params = json.RawMessage("[]")
	}
	if h.p2p == nil {
		http.Error(w, "p2p unavailable", http.StatusServiceUnavailable)
		return
	}
	inc(&h.metrics.P2PAttempts)
	result, err := h.p2p.CallPeer(r.Context(), probe.PeerID, jsonrpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  probe.Method,
		Params:  probe.Params,
	})
	if err != nil {
		inc(&h.metrics.P2PFailures)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	inc(&h.metrics.P2PSuccesses)
	writeJSON(w, map[string]any{"ok": true, "peer_id": probe.PeerID, "method": probe.Method, "result": result})
}

func (h *Handler) authorizeAdmin(w http.ResponseWriter, r *http.Request) bool {
	if h.cfg.AdminToken != "" {
		if r.Header.Get("X-Admin-Token") == h.cfg.AdminToken || r.Header.Get("Authorization") == "Bearer "+h.cfg.AdminToken {
			return true
		}
		http.Error(w, "admin token required", http.StatusUnauthorized)
		return false
	}
	if isLocalRequest(r) {
		return true
	}
	http.Error(w, "admin disabled for non-local clients; set admin_token", http.StatusForbidden)
	return false
}

func isLocalRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

func firstNonSpace(b []byte) byte {
	for _, c := range b {
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
			return c
		}
	}
	return 0
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
