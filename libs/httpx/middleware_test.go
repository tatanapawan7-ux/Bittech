package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tatanapawan7-ux/bittech/libs/ratelimit"
)

func TestRateLimitReturns429(t *testing.T) {
	lim := ratelimit.NewMemory(ratelimit.Config{Rate: 1, Burst: 2})
	var limited int
	mw := RateLimit(lim, func(*http.Request) string { return "fixed" }, func() { limited++ })
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))

	codes := []int{}
	for i := 0; i < 4; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
		codes = append(codes, rec.Code)
	}
	// Burst 2 allowed, then 429s.
	if codes[0] != 200 || codes[1] != 200 || codes[2] != 429 || codes[3] != 429 {
		t.Fatalf("unexpected codes: %v", codes)
	}
	if limited != 2 {
		t.Fatalf("limited callback count = %d, want 2", limited)
	}
}

func TestRateLimitFailsOpenOnError(t *testing.T) {
	mw := RateLimit(errLimiter{}, func(*http.Request) string { return "k" }, nil)
	rec := httptest.NewRecorder()
	served := false
	mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { served = true })).
		ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if !served {
		t.Fatal("should fail open and serve the request on limiter error")
	}
}

type errLimiter struct{}

func (errLimiter) Allow(context.Context, string) (bool, error) {
	return false, context.DeadlineExceeded
}

func TestMetricsInstrument(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/thing", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(201) })
	h := m.Instrument(mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/thing", nil))
	if rec.Code != 201 {
		t.Fatalf("status %d", rec.Code)
	}
	// The matched pattern is used as the route label.
	got := testCounterValue(t, reg, "http_requests_total")
	if got < 1 {
		t.Fatalf("expected http_requests_total >= 1, got %v", got)
	}
}

func testCounterValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			var sum float64
			for _, m := range mf.GetMetric() {
				sum += m.GetCounter().GetValue()
			}
			return sum
		}
	}
	return 0
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/x", nil)
	r.RemoteAddr = "10.0.0.5:54321"
	if ip := ClientIP(r); ip != "10.0.0.5" {
		t.Fatalf("ClientIP = %q", ip)
	}
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	if ip := ClientIP(r); ip != "203.0.113.7" {
		t.Fatalf("ClientIP with XFF = %q", ip)
	}
}
