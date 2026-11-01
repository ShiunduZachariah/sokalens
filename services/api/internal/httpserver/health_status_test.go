package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/opshealth"
)

type stubFreshness struct {
	snapshot opshealth.Snapshot
	calls    int
}

func (s *stubFreshness) Snapshot(context.Context) opshealth.Snapshot {
	s.calls++
	return s.snapshot
}

func healthBody(t *testing.T, opts ServerOptions) (int, map[string]any, http.Header) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	New(opts).ServeHTTP(rec, req)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (%s)", err, rec.Body.String())
	}
	return rec.Code, body, rec.Header()
}

func TestHealthzReportsDependenciesAndFreshness(t *testing.T) {
	at := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	seconds := int64(45)
	freshness := &stubFreshness{snapshot: opshealth.Snapshot{
		Dependencies: map[string]bool{"postgres": true, "redis": true},
		LastLivePoll: opshealth.Age{At: &at, Seconds: &seconds},
	}}

	code, body, header := healthBody(t, ServerOptions{
		Config:          config.Config{Environment: "test", Port: 8080},
		HealthFreshness: freshness,
	})

	if code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("code = %d, body = %v", code, body)
	}
	dependencies, _ := body["dependencies"].(map[string]any)
	if dependencies["postgres"] != true || dependencies["redis"] != true {
		t.Fatalf("dependencies = %v", dependencies)
	}
	livePoll, _ := body["last_live_poll"].(map[string]any)
	if livePoll["age_seconds"] != float64(45) {
		t.Fatalf("last_live_poll = %v", livePoll)
	}
	// A health document served from a cache is a statement about the past
	// presented as one about the present.
	if got := header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

// One read covers both the probes and the ages, and that read is cached.
// The probes are the expensive part -- a database ping and a socket dial --
// and running them per request on an unauthenticated endpoint would make it
// the free database lever the cache exists to prevent.
func TestHealthzCostsOneSourceReadPerRequest(t *testing.T) {
	freshness := &stubFreshness{snapshot: opshealth.Snapshot{Dependencies: map[string]bool{"postgres": true}}}
	handler := New(ServerOptions{
		Config:          config.Config{Environment: "test", Port: 8080},
		HealthFreshness: freshness,
	})
	for range 5 {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	}
	if freshness.calls != 5 {
		t.Fatalf("source reads = %d, want one per request", freshness.calls)
	}
}

// The distinction from /readyz is the whole point. /readyz fails closed and is
// what the container healthcheck and the deploy gate read; making this fail
// closed too would duplicate it, and a database blip could start restarting
// containers. This one stays 200 while the process is alive, so a watchdog
// pinging it separates a dead host from a degraded one.
func TestHealthzStaysOKWhenADependencyIsDown(t *testing.T) {
	freshness := &stubFreshness{snapshot: opshealth.Snapshot{
		Dependencies: map[string]bool{"postgres": false, "redis": true},
	}}
	code, body, _ := healthBody(t, ServerOptions{
		Config:          config.Config{Environment: "test", Port: 8080},
		HealthFreshness: freshness,
	})

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 while the process is alive", code)
	}
	if body["status"] != "degraded" {
		t.Fatalf("status = %v, want degraded", body["status"])
	}
	dependencies, _ := body["dependencies"].(map[string]any)
	if dependencies["postgres"] != false {
		t.Fatalf("dependencies = %v", dependencies)
	}
}

func TestHealthzOmitsFreshnessItDoesNotHave(t *testing.T) {
	// A database that is down yields no ages. Reporting them as absent is
	// honest; reporting zeroes would read as "just happened".
	freshness := &stubFreshness{snapshot: opshealth.Snapshot{
		Dependencies: map[string]bool{"postgres": false},
	}}
	_, body, _ := healthBody(t, ServerOptions{
		Config:          config.Config{Environment: "test", Port: 8080},
		HealthFreshness: freshness,
	})
	if _, present := body["last_live_poll"]; present {
		t.Fatalf("freshness reported without a value: %v", body)
	}
}

func TestHealthzWithoutAFreshnessSourceStillReportsLiveness(t *testing.T) {
	// A deployment that has not wired the source degrades to the previous
	// behaviour rather than failing.
	code, body, _ := healthBody(t, ServerOptions{Config: config.Config{Environment: "test", Port: 8080}})
	if code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("code = %d, body = %v", code, body)
	}
}

// It is unauthenticated, so it must leak nothing an observer could not already
// infer. The snapshot carries booleans by construction, so there is no error
// text for the handler to render even by accident.
func TestHealthzLeaksNoDiagnosticDetail(t *testing.T) {
	freshness := &stubFreshness{snapshot: opshealth.Snapshot{
		Dependencies: map[string]bool{"postgres": false, "redis": false},
	}}
	rec := httptest.NewRecorder()
	New(ServerOptions{
		Config:          config.Config{Environment: "test", Port: 8080},
		HealthFreshness: freshness,
	}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	for _, secret := range []string{"10.0.0.4", "5432", "connection refused", "dial tcp", "password"} {
		if got := rec.Body.String(); strings.Contains(got, secret) {
			t.Fatalf("body leaked %q: %s", secret, got)
		}
	}
}
