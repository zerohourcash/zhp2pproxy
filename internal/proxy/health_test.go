package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zerohourcash/zhp2pproxy/internal/config"
	"github.com/zerohourcash/zhp2pproxy/internal/node"
	"github.com/zerohourcash/zhp2pproxy/pkg/jsonrpc"
)

type healthTestP2P struct {
	peers      int
	lastPeerID string
	lastMethod string
}

func (p *healthTestP2P) CallAny(context.Context, jsonrpc.Request) (json.RawMessage, error) {
	return json.RawMessage("1"), nil
}

func (p *healthTestP2P) CallPeer(_ context.Context, peerID string, req jsonrpc.Request) (json.RawMessage, error) {
	p.lastPeerID = peerID
	p.lastMethod = req.Method
	return json.RawMessage("1"), nil
}

func (p *healthTestP2P) Info() any                                 { return nil }
func (p *healthTestP2P) AdminState() any                           { return nil }
func (p *healthTestP2P) ConnectPeer(context.Context, string) error { return nil }
func (p *healthTestP2P) Publish(context.Context, []byte) error     { return nil }
func (p *healthTestP2P) PeerID() string                            { return "test-peer-id" }
func (p *healthTestP2P) NetworkID() string                         { return "test-network" }
func (p *healthTestP2P) ConnectedPeerCount() int                   { return p.peers }
func (p *healthTestP2P) NodeP2PStatus() any                        { return map[string]any{"enabled": true} }

func TestHealthReportsLocalNodeAndP2PConnectivity(t *testing.T) {
	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":1679836,"id":1}`))
	}))
	defer rpcServer.Close()

	cfg := config.Default()
	cfg.RPCURL = rpcServer.URL
	handler := NewHandler(cfg, node.NewClient(cfg), &healthTestP2P{peers: 2}, &Metrics{})
	recorder := httptest.NewRecorder()
	handler.Health(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("Health status = %d, want 200", recorder.Code)
	}
	var response struct {
		OK        bool `json:"ok"`
		LocalNode struct {
			Connected   bool    `json:"connected"`
			BlockHeight float64 `json:"block_height"`
			Optional    bool    `json:"optional"`
		} `json:"local_node"`
		P2PNetwork struct {
			Connected      bool   `json:"connected"`
			ConnectedPeers int    `json:"connected_peers"`
			NetworkID      string `json:"network_id"`
			PeerID         string `json:"peer_id"`
		} `json:"p2p_network"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || !response.LocalNode.Connected || !response.LocalNode.Optional {
		t.Fatalf("unexpected local health: %+v", response)
	}
	if response.LocalNode.BlockHeight != 1679836 {
		t.Fatalf("block height = %v, want 1679836", response.LocalNode.BlockHeight)
	}
	if !response.P2PNetwork.Connected || response.P2PNetwork.ConnectedPeers != 2 {
		t.Fatalf("unexpected P2P health: %+v", response.P2PNetwork)
	}
	if response.P2PNetwork.NetworkID != "test-network" || response.P2PNetwork.PeerID != "test-peer-id" {
		t.Fatalf("unexpected P2P identity: %+v", response.P2PNetwork)
	}
}

func TestHealthRejectsPost(t *testing.T) {
	cfg := config.Default()
	handler := NewHandler(cfg, node.NewClient(cfg), nil, &Metrics{})
	recorder := httptest.NewRecorder()
	handler.Health(recorder, httptest.NewRequest(http.MethodPost, "/health", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("Health status = %d, want 405", recorder.Code)
	}
}
