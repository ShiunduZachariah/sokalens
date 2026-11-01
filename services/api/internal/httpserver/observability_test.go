package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/admin"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/config"
)

type observabilityAdminAuth struct {
	valid bool
}

func (a observabilityAdminAuth) Login(context.Context, string, string) (admin.LoginResult, error) {
	return admin.LoginResult{}, nil
}

func (a observabilityAdminAuth) ValidateSession(context.Context, string) (admin.User, error) {
	if !a.valid {
		return admin.User{}, admin.ErrInvalidSession
	}
	return admin.User{ID: 1, Role: admin.RoleAnalyst}, nil
}

func (a observabilityAdminAuth) Logout(context.Context, string) error { return nil }

func TestObservabilityMiddlewareRecordsBoundedRoutesAndErrors(t *testing.T) {
	metrics := newRequestMetrics()
	handler := observabilityMiddleware(metrics, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "fail") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	for _, path := range []string{"/v1/matches/82163", "/untrusted/customer-specific-value", "/fail"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	snapshot := metrics.snapshot(time.Now())
	if snapshot.Requests != 3 || snapshot.ServerErrors != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if got := metricRoute("/v1/matches/82163"); got != "/v1/matches/:id" {
		t.Fatalf("dynamic route = %q", got)
	}
	if got := metricRoute("/anything/attacker-controlled"); got != "other" {
		t.Fatalf("unknown route leaked into metric cardinality: %q", got)
	}
}

func TestObservabilityEndpointsRequireAnalyticsPermission(t *testing.T) {
	metrics := newRequestMetrics()
	metrics.observe(http.MethodGet, "/v1/matches/today", http.StatusOK, 125*time.Millisecond)

	unauthorized := httptest.NewRecorder()
	observabilityMetricsHandler(observabilityAdminAuth{}, metrics).ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/admin/observability/metrics", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/admin/observability/metrics", nil)
	request.Header.Set("Authorization", "Bearer admin-session")
	response := httptest.NewRecorder()
	observabilityMetricsHandler(observabilityAdminAuth{valid: true}, metrics).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `route="/v1/matches/today"`) || strings.Contains(response.Body.String(), "admin-session") {
		t.Fatalf("metrics response = %d %s", response.Code, response.Body.String())
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "private, no-store" {
		t.Fatalf("cache control = %q", cacheControl)
	}
}

func TestObservabilityDiagnosticsAndHealthAliasHandlers(t *testing.T) {
	metrics := newRequestMetrics()
	metrics.observe(http.MethodGet, "/ready", http.StatusOK, 10*time.Millisecond)
	request := httptest.NewRequest(http.MethodGet, "/v1/admin/observability/diagnostics", nil)
	request.Header.Set("Authorization", "Bearer admin-session")
	response := httptest.NewRecorder()
	observabilityDiagnosticsHandler(observabilityAdminAuth{valid: true}, metrics).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"requests":1`) || !strings.Contains(response.Body.String(), `"approx_p95_ms":50`) {
		t.Fatalf("diagnostics response = %d %s", response.Code, response.Body.String())
	}

	server := New(ServerOptions{Config: config.Config{Environment: "test"}})
	healthResponse := httptest.NewRecorder()
	server.ServeHTTP(healthResponse, httptest.NewRequest(http.MethodGet, "/health", nil))
	if healthResponse.Code != http.StatusOK {
		t.Fatalf("health alias route = %d %s", healthResponse.Code, healthResponse.Body.String())
	}
	readyResponse := httptest.NewRecorder()
	server.ServeHTTP(readyResponse, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if readyResponse.Code != http.StatusOK {
		t.Fatalf("ready alias route = %d %s", readyResponse.Code, readyResponse.Body.String())
	}
}
