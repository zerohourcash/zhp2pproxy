package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"zhp2pproxy/internal/config"
	"zhp2pproxy/internal/node"
)

func TestAdminProbeTargetsRequestedPeer(t *testing.T) {
	cfg := config.Default()
	p2p := &healthTestP2P{peers: 1}
	handler := NewHandler(cfg, node.NewClient(cfg), p2p, &Metrics{})
	body := bytes.NewBufferString(`{"peer_id":"12D3KooWTest","method":"getblockcount","params":[]}`)
	request := httptest.NewRequest(http.MethodPost, "/admin/probe", body)
	request.RemoteAddr = "127.0.0.1:12345"
	recorder := httptest.NewRecorder()
	handler.AdminProbe(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		OK     bool    `json:"ok"`
		Result float64 `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Result != 1 {
		t.Fatalf("unexpected response: %+v", response)
	}
	if p2p.lastPeerID != "12D3KooWTest" || p2p.lastMethod != "getblockcount" {
		t.Fatalf("probe was routed incorrectly: peer=%q method=%q", p2p.lastPeerID, p2p.lastMethod)
	}
}
