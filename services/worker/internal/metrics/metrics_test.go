package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegistryRendersCountersAndGauges(t *testing.T) {
	registry := New()
	registry.IncCounter("worker_requests_deduplicated_total")
	registry.IncCounter("worker_requests_deduplicated_total")
	registry.SetGauge("worker_quota_daily_remaining", 4123)
	registry.SetGauge("worker_quota_backfill_paused", 0)

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("content-type = %q, want prometheus text", recorder.Header().Get("Content-Type"))
	}
	text := string(body)
	for _, expected := range []string{
		"# TYPE worker_requests_deduplicated_total counter",
		"worker_requests_deduplicated_total 2",
		"# TYPE worker_quota_daily_remaining gauge",
		"worker_quota_daily_remaining 4123",
		"worker_quota_backfill_paused 0",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("metrics output missing %q:\n%s", expected, text)
		}
	}
}

func TestSafeMetricName(t *testing.T) {
	cases := map[string]string{
		"":            "unknown",
		"team_history": "team_history",
		"live":         "live",
		"2nd half":     "_2nd_half",
		"pre-match!?":  "pre_match__",
	}
	for input, want := range cases {
		if got := SafeMetricName(input); got != want {
			t.Fatalf("SafeMetricName(%q) = %q, want %q", input, got, want)
		}
	}
}
