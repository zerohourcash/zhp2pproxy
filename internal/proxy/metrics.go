package proxy

import "sync/atomic"

type Metrics struct {
	RequestsTotal    uint64 `json:"requests_total"`
	LocalSuccesses   uint64 `json:"local_successes"`
	LocalFailures    uint64 `json:"local_failures"`
	P2PAttempts      uint64 `json:"p2p_attempts"`
	P2PSuccesses     uint64 `json:"p2p_successes"`
	P2PFailures      uint64 `json:"p2p_failures"`
	MethodDenied     uint64 `json:"method_denied"`
	BadRequests      uint64 `json:"bad_requests"`
	RateLimitedHTTP  uint64 `json:"rate_limited_http"`
	UnauthorizedHTTP uint64 `json:"unauthorized_http"`
	ForbiddenHTTP    uint64 `json:"forbidden_http"`
}

type MetricsSnapshot = Metrics

func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		RequestsTotal:    atomic.LoadUint64(&m.RequestsTotal),
		LocalSuccesses:   atomic.LoadUint64(&m.LocalSuccesses),
		LocalFailures:    atomic.LoadUint64(&m.LocalFailures),
		P2PAttempts:      atomic.LoadUint64(&m.P2PAttempts),
		P2PSuccesses:     atomic.LoadUint64(&m.P2PSuccesses),
		P2PFailures:      atomic.LoadUint64(&m.P2PFailures),
		MethodDenied:     atomic.LoadUint64(&m.MethodDenied),
		BadRequests:      atomic.LoadUint64(&m.BadRequests),
		RateLimitedHTTP:  atomic.LoadUint64(&m.RateLimitedHTTP),
		UnauthorizedHTTP: atomic.LoadUint64(&m.UnauthorizedHTTP),
		ForbiddenHTTP:    atomic.LoadUint64(&m.ForbiddenHTTP),
	}
}

func inc(v *uint64) {
	atomic.AddUint64(v, 1)
}
