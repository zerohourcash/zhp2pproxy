package server

import (
	"net/http"

	"github.com/zerohourcash/zhp2pproxy/internal/config"
	"github.com/zerohourcash/zhp2pproxy/internal/proxy"
)

func New(cfg config.Config, h *proxy.Handler, metrics *proxy.Metrics) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.RPC)
	mux.HandleFunc("/zhp2p/rpc", h.BrowserRPC)
	mux.HandleFunc("/info", h.Info)
	mux.HandleFunc("/health", h.Health)
	mux.HandleFunc("/admin/status", h.AdminStatus)
	mux.HandleFunc("/admin/connect", h.AdminConnect)
	mux.HandleFunc("/admin/probe", h.AdminProbe)
	mux.HandleFunc("/pubsub/publish", h.Publish)
	return middleware(cfg, metrics, mux)
}
