package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"zhp2pproxy/internal/config"
	"zhp2pproxy/internal/proxy"
)

func middleware(cfg config.Config, metrics *proxy.Metrics, next http.Handler) http.Handler {
	limiters := &clientLimiters{items: make(map[string]*clientLimiter)}
	allowed := make(map[string]struct{}, len(cfg.AllowedIPs))
	for _, ip := range cfg.AllowedIPs {
		allowed[ip] = struct{}{}
	}
	origins := make(map[string]struct{}, len(cfg.CORSAllowedOrigins))
	allowAllOrigins := false
	for _, origin := range cfg.CORSAllowedOrigins {
		if origin == "*" {
			allowAllOrigins = true
		}
		origins[origin] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowAllOrigins {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if _, ok := origins[origin]; ok {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Token")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		ip := clientIP(r)
		if len(allowed) > 0 {
			if _, ok := allowed[ip]; !ok {
				proxyIncForbidden(metrics)
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		if cfg.APIToken != "" && !strings.HasPrefix(r.URL.Path, "/admin/") && !validToken(r, cfg.APIToken) {
			proxyIncUnauthorized(metrics)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !limiters.get(ip, cfg).Allow() {
			proxyIncRateLimited(metrics)
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type clientLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type clientLimiters struct {
	mu    sync.Mutex
	items map[string]*clientLimiter
}

func (c *clientLimiters) get(ip string, cfg config.Config) *rate.Limiter {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if item, ok := c.items[ip]; ok {
		item.lastSeen = now
		return item.limiter
	}
	item := &clientLimiter{
		limiter:  rate.NewLimiter(rate.Limit(cfg.RateLimit), cfg.RateBurst),
		lastSeen: now,
	}
	c.items[ip] = item
	if len(c.items) > 1024 {
		for key, val := range c.items {
			if now.Sub(val.lastSeen) > 10*time.Minute {
				delete(c.items, key)
			}
		}
	}
	return item.limiter
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ip := strings.TrimSpace(strings.Split(xff, ",")[0])
		if ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func validToken(r *http.Request, token string) bool {
	if r.Header.Get("X-API-Token") == token {
		return true
	}
	auth := r.Header.Get("Authorization")
	return auth == "Bearer "+token
}

func proxyIncRateLimited(m *proxy.Metrics) {
	atomic.AddUint64(&m.RateLimitedHTTP, 1)
}

func proxyIncUnauthorized(m *proxy.Metrics) {
	atomic.AddUint64(&m.UnauthorizedHTTP, 1)
}

func proxyIncForbidden(m *proxy.Metrics) {
	atomic.AddUint64(&m.ForbiddenHTTP, 1)
}
