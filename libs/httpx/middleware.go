// Package httpx holds cross-cutting HTTP middleware for the API edge:
// request metrics and rate limiting. Kept backend-agnostic so every service
// composes the same observability and protection.
package httpx

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tatanapawan7-ux/bittech/libs/ratelimit"
)

// Metrics holds the Prometheus collectors for the HTTP edge.
type Metrics struct {
	requests *prometheus.CounterVec
	latency  *prometheus.HistogramVec
	limited  prometheus.Counter
}

// NewMetrics registers and returns the HTTP metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests by method, route, and status.",
		}, []string{"method", "route", "status"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency by route.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route"}),
		limited: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "http_rate_limited_total",
			Help: "Requests rejected by the rate limiter.",
		}),
	}
	reg.MustRegister(m.requests, m.latency, m.limited)
	return m
}

// statusRecorder captures the response status for metrics.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Instrument wraps a handler to record request count and latency. The route
// label is the matched pattern (low cardinality), never the raw path.
func (m *Metrics) Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := routeLabel(r)
		m.requests.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
		m.latency.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}

// routeLabel returns the matched ServeMux pattern when available, falling back
// to "other" so unmatched paths can't explode metric cardinality.
func routeLabel(r *http.Request) string {
	if p := r.Pattern; p != "" {
		return p
	}
	return "other"
}

// KeyFunc derives the rate-limit key for a request (e.g. client IP or user id).
type KeyFunc func(*http.Request) string

// RateLimit rejects requests over the limit with 429. observeLimited increments
// the limiter metric (pass m.MarkLimited).
func RateLimit(lim ratelimit.Limiter, key KeyFunc, onLimited func()) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, err := lim.Allow(r.Context(), key(r))
			if err != nil {
				// Fail open on limiter errors: availability over strictness, but
				// the error is surfaced via logs/metrics by the caller.
				next.ServeHTTP(w, r)
				return
			}
			if !ok {
				if onLimited != nil {
					onLimited()
				}
				w.Header().Set("Retry-After", "1")
				http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// MarkLimited returns a callback that increments the rate-limited counter.
func (m *Metrics) MarkLimited() func() { return func() { m.limited.Inc() } }

// ClientIP extracts the client IP for use as a rate-limit key, honoring a
// single X-Forwarded-For hop (the load balancer) when present.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if host, _, err := net.SplitHostPort(xff); err == nil {
			return host
		}
		return xff
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
