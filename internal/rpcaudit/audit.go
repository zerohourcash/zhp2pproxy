package rpcaudit

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	defaultSource = "internal"
	defaultRoute  = "unknown"
	defaultTarget = "unavailable"
	maxFieldRunes = 160
)

type contextKey struct{}

// Logger matches log.Printf without coupling the audit collector to the global
// logger. Tests can provide an in-memory logger.
type Logger func(format string, args ...any)

type Event struct {
	Method   string
	Source   string
	Route    string
	Target   string
	Duration time.Duration
	Success  bool
}

type bucket struct {
	Requests uint64
	Errors   uint64
}

type window struct {
	started      time.Time
	requests     uint64
	errors       uint64
	totalLatency time.Duration
	maxLatency   time.Duration
	routes       map[string]bucket
	methods      map[string]uint64
	sources      map[string]uint64
}

type Tracker struct {
	mu     sync.Mutex
	logf   Logger
	window window
}

func New(logf Logger) *Tracker {
	now := time.Now()
	return &Tracker{logf: logf, window: newWindow(now)}
}

func WithSource(ctx context.Context, source string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKey{}, cleanField(source, defaultSource))
}

func Source(ctx context.Context) string {
	if ctx != nil {
		if source, ok := ctx.Value(contextKey{}).(string); ok && source != "" {
			return cleanField(source, defaultSource)
		}
	}
	return defaultSource
}

// Record prints one privacy-safe routing line and adds it to the current
// reporting window. RPC parameters, response bodies, credentials and request
// IDs are deliberately excluded.
func (t *Tracker) Record(event Event) {
	if t == nil {
		return
	}
	event.Method = cleanField(event.Method, "missing")
	event.Source = cleanField(event.Source, defaultSource)
	event.Route = cleanField(event.Route, defaultRoute)
	event.Target = cleanField(event.Target, defaultTarget)
	if event.Duration < 0 {
		event.Duration = 0
	}
	status := "ok"
	if !event.Success {
		status = "error"
	}
	if t.logf != nil {
		t.logf("[rpc] method=%s source=%s route=%s target=%s status=%s duration_ms=%d",
			event.Method, event.Source, event.Route, event.Target, status, event.Duration.Milliseconds())
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.window.requests++
	if !event.Success {
		t.window.errors++
	}
	t.window.totalLatency += event.Duration
	if event.Duration > t.window.maxLatency {
		t.window.maxLatency = event.Duration
	}
	routeStats := t.window.routes[event.Route]
	routeStats.Requests++
	if !event.Success {
		routeStats.Errors++
	}
	t.window.routes[event.Route] = routeStats
	t.window.methods[event.Method]++
	t.window.sources[aggregateSource(event.Source)]++
}

// Start emits and resets an aggregate report every interval. Production uses
// one hour; accepting an interval keeps the behavior deterministic in tests.
func (t *Tracker) Start(ctx context.Context, interval time.Duration) {
	if t == nil {
		return
	}
	if interval <= 0 {
		interval = time.Hour
	}
	if t.logf != nil {
		t.logf("[rpc-stats] enabled interval=%s; individual calls and rolling load summaries exclude params and credentials", interval)
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				t.reportAndReset(now)
			}
		}
	}()
}

// ReportNow is useful for an operator-triggered report and for tests. It
// reports the current partial window and begins a new one.
func (t *Tracker) ReportNow() {
	if t != nil {
		t.reportAndReset(time.Now())
	}
}

func (t *Tracker) reportAndReset(now time.Time) {
	t.mu.Lock()
	current := t.window
	t.window = newWindow(now)
	t.mu.Unlock()

	windowDuration := now.Sub(current.started).Round(time.Second)
	if windowDuration < 0 {
		windowDuration = 0
	}
	ok := current.requests - current.errors
	var average time.Duration
	if current.requests > 0 {
		average = current.totalLatency / time.Duration(current.requests)
	}
	if t.logf != nil {
		t.logf("[rpc-stats] window=%s requests=%d ok=%d errors=%d avg_ms=%d max_ms=%d routes=%s sources=%s methods=%s",
			windowDuration, current.requests, ok, current.errors, average.Milliseconds(),
			current.maxLatency.Milliseconds(), formatBuckets(current.routes),
			formatCounts(current.sources), formatCounts(current.methods))
	}
}

func newWindow(started time.Time) window {
	return window{
		started: started,
		routes:  make(map[string]bucket),
		methods: make(map[string]uint64),
		sources: make(map[string]uint64),
	}
}

func formatBuckets(values map[string]bucket) string {
	if len(values) == 0 {
		return "none"
	}
	keys := sortedKeys(values)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := values[key]
		parts = append(parts, fmt.Sprintf("%s:%d/%d", key, value.Requests, value.Errors))
	}
	return strings.Join(parts, ",")
}

func formatCounts(values map[string]uint64) string {
	if len(values) == 0 {
		return "none"
	}
	keys := sortedKeys(values)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", key, values[key]))
	}
	return strings.Join(parts, ",")
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cleanField(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	runes := []rune(value)
	if len(runes) > maxFieldRunes {
		runes = runes[:maxFieldRunes]
	}
	for i, char := range runes {
		if unicode.IsSpace(char) || unicode.IsControl(char) || char == ',' || char == '=' {
			runes[i] = '_'
		}
	}
	return string(runes)
}

func aggregateSource(source string) string {
	for _, prefix := range []string{"overlay-peer:", "browser-peer:"} {
		if strings.HasPrefix(source, prefix) {
			return strings.TrimSuffix(prefix, ":")
		}
	}
	return source
}
