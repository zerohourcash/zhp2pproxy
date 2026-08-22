package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/zerohourcash/zhp2pproxy/internal/config"
	"github.com/zerohourcash/zhp2pproxy/internal/proxy"
)

func middleware(cfg config.Config, metrics *proxy.Metrics, next http.Handler) http.Handler {
	limiters := &clientLimiters{items: make(map[string]*clientLimiter)}
	allowed := make(map[string]struct{}, len(cfg.AllowedIPs))
	for _, ip := range cfg.AllowedIPs {
		allowed[ip] = struct{}{}
	}
	origins := make(map[string]struct{}, len(cfg.CORSAllowedOrigins))
	trustedProxies := parseNetworks(cfg.TrustedProxies)
	allowAllOrigins := false
	for _, origin := range cfg.CORSAllowedOrigins {
		if origin == "*" {
			allowAllOrigins = true
		}
		origins[origin] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		origin := r.Header.Get("Origin")
		originAllowed := origin == "" || allowAllOrigins
		if _, ok := origins[origin]; ok {
			originAllowed = true
		}
		if !originAllowed {
			proxyIncForbidden(metrics)
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		if allowAllOrigins {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if _, ok := origins[origin]; ok {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Token, X-Admin-Token")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		ip := clientIP(r, trustedProxies)
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
	for len(c.items) > 4096 {
		var oldestKey string
		var oldest time.Time
		for key, val := range c.items {
			if oldestKey == "" || val.lastSeen.Before(oldest) {
				oldestKey = key
				oldest = val.lastSeen
			}
		}
		delete(c.items, oldestKey)
	}
	return item.limiter
}

func clientIP(r *http.Request, trustedProxies []*net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remoteIP := net.ParseIP(strings.TrimSpace(host))
	if remoteIP != nil && ipInNetworks(remoteIP, trustedProxies) {
		xff := r.Header.Get("X-Forwarded-For")
		ip := strings.TrimSpace(strings.Split(xff, ",")[0])
		if parsed := net.ParseIP(ip); parsed != nil {
			return ip
		}
	}
	return host
}

func parseNetworks(entries []string) []*net.IPNet {
	var networks []*net.IPNet
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			bits := 128
			if ip.To4() != nil {
				bits = 32
			}
			networks = append(networks, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		if _, network, err := net.ParseCIDR(entry); err == nil {
			networks = append(networks, network)
		}
	}
	return networks
}

func ipInNetworks(ip net.IP, networks []*net.IPNet) bool {
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func validToken(r *http.Request, token string) bool {
	if constantTimeEqual(r.Header.Get("X-API-Token"), token) {
		return true
	}
	auth := r.Header.Get("Authorization")
	return constantTimeEqual(auth, "Bearer "+token)
}

func constantTimeEqual(got, expected string) bool {
	gotHash := sha256.Sum256([]byte(got))
	expectedHash := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(gotHash[:], expectedHash[:]) == 1
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
