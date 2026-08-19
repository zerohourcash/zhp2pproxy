package server

import (
	"net/http"

	"zhp2pproxy/internal/config"
	"zhp2pproxy/internal/proxy"
)

func New(cfg config.Config, h *proxy.Handler, metrics *proxy.Metrics) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.RPC)
	mux.HandleFunc("/info", h.Info)
	mux.HandleFunc("/admin/status", h.AdminStatus)
	mux.HandleFunc("/admin/connect", h.AdminConnect)
	mux.HandleFunc("/pubsub/publish", h.Publish)
	return middleware(cfg, metrics, mux)
}
