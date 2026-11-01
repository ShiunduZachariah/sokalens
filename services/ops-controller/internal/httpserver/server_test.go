package httpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/alerts"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telemetry"
)

func testServer(t *testing.T) (*Server, *telemetry.Metrics) {
	t.Helper()
	cfg, err := config.Load(map[string]string{"OPS_SHUTDOWN_TIMEOUT": "1s"})
	if err != nil {
		t.Fatal(err)
	}
	metrics := telemetry.NewMetrics()
	server := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), metrics)
	return server, metrics
}

func TestSignedAlertIngestionAcceptsTypedDataWithoutMutation(t *testing.T) {
	cfg, err := config.Load(map[string]string{"OPS_ALERT_SIGNING_SECRET": "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	var captured alerts.Accepted
	sink := alerts.SinkFunc(func(_ context.Context, got alerts.Accepted) error { captured = got; return nil })
	server := NewWithAlertSink(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), telemetry.NewMetrics(), sink)
	body := []byte(`{"version":"v1","alert_id":"alert-1","source":"prometheus","name":"api_latency","service":"api","environment":"staging","severity":"critical","starts_at":"2026-08-14T10:00:00Z","summary":"API latency is above its SLO."}`)
	timestamp := time.Now().UTC().Format(time.RFC3339)
	makeRequest := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/alerts", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Ops-Timestamp", timestamp)
		req.Header.Set("X-Ops-Delivery-ID", "delivery-1")
		req.Header.Set("X-Ops-Signature", alerts.Sign(cfg.AlertSigningSecret, timestamp, body))
		return req
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, makeRequest())
	if response.Code != http.StatusAccepted || captured.AlertID != "alert-1" || !strings.Contains(response.Body.String(), "idempotency_key") {
		t.Fatalf("response=%d %s captured=%+v", response.Code, response.Body.String(), captured)
	}

	replay := httptest.NewRecorder()
	server.Handler().ServeHTTP(replay, makeRequest())
	if replay.Code != http.StatusConflict {
		t.Fatalf("replay status=%d want 409", replay.Code)
	}
}

func TestAlertIngestionRejectsDisabledOversizeAndRetriesSinkFailure(t *testing.T) {
	disabled, _ := config.Load(map[string]string{})
	server := New(disabled, slog.New(slog.NewTextHandler(io.Discard, nil)), telemetry.NewMetrics())
	req := httptest.NewRequest(http.MethodPost, "/v1/alerts", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled status=%d", response.Code)
	}

	cfg, _ := config.Load(map[string]string{"OPS_ALERT_SIGNING_SECRET": "0123456789abcdef0123456789abcdef", "OPS_ALERT_MAX_BODY_BYTES": "1024"})
	server = New(cfg, nil, nil)
	over := httptest.NewRequest(http.MethodPost, "/v1/alerts", strings.NewReader(strings.Repeat("x", 1025)))
	over.Header.Set("Content-Type", "application/json")
	overResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(overResponse, over)
	if overResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status=%d", overResponse.Code)
	}

	body := []byte(`{"version":"v1","alert_id":"alert-1","source":"prometheus","name":"api_latency","service":"api","environment":"staging","severity":"critical","starts_at":"2026-08-14T10:00:00Z","summary":"API latency is above its SLO."}`)
	timestamp := time.Now().UTC().Format(time.RFC3339)
	failing := NewWithAlertSink(cfg, nil, nil, alerts.SinkFunc(func(context.Context, alerts.Accepted) error { return errors.New("store unavailable") }))
	makeRequest := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/alerts", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Ops-Timestamp", timestamp)
		r.Header.Set("X-Ops-Delivery-ID", "retry-delivery")
		r.Header.Set("X-Ops-Signature", alerts.Sign(cfg.AlertSigningSecret, timestamp, body))
		return r
	}
	for i := 0; i < 2; i++ {
		got := httptest.NewRecorder()
		failing.Handler().ServeHTTP(got, makeRequest())
		if got.Code != http.StatusServiceUnavailable {
			t.Fatalf("attempt %d status=%d", i, got.Code)
		}
	}
}

func TestAlertReplayCapacityFailsUnavailableNotDuplicate(t *testing.T) {
	cfg, _ := config.Load(map[string]string{"OPS_ALERT_SIGNING_SECRET": "0123456789abcdef0123456789abcdef"})
	server := NewWithAlertSink(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), telemetry.NewMetrics(), alerts.SinkFunc(func(context.Context, alerts.Accepted) error { return nil }))
	server.alertValidator = alerts.NewValidator(cfg.AlertSigningSecret, cfg.AlertMaxClockSkew, 1)
	body := []byte(`{"version":"v1","alert_id":"alert-1","source":"prometheus","name":"api_latency","service":"api","environment":"staging","severity":"critical","starts_at":"2026-08-14T10:00:00Z","summary":"API latency is above its SLO."}`)
	timestamp := time.Now().UTC().Format(time.RFC3339)
	request := func(delivery string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/alerts", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Ops-Timestamp", timestamp)
		r.Header.Set("X-Ops-Delivery-ID", delivery)
		r.Header.Set("X-Ops-Signature", alerts.Sign(cfg.AlertSigningSecret, timestamp, body))
		return r
	}
	first := httptest.NewRecorder()
	server.Handler().ServeHTTP(first, request("delivery-1"))
	full := httptest.NewRecorder()
	server.Handler().ServeHTTP(full, request("delivery-2"))
	if first.Code != http.StatusAccepted || full.Code != http.StatusServiceUnavailable {
		t.Fatalf("first=%d capacity=%d", first.Code, full.Code)
	}
}

func TestConfiguredTransportWithoutDurableSinkFailsClosed(t *testing.T) {
	cfg, err := config.Load(map[string]string{"OPS_ALERT_SIGNING_SECRET": "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	server := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), telemetry.NewMetrics())
	body := []byte(`{"version":"v1","alert_id":"alert-1","source":"prometheus","name":"api_latency","service":"api","environment":"staging","severity":"critical","starts_at":"2026-08-14T10:00:00Z","summary":"API latency is above its SLO."}`)
	timestamp := time.Now().UTC().Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodPost, "/v1/alerts", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ops-Timestamp", timestamp)
	req.Header.Set("X-Ops-Delivery-ID", "delivery-without-sink")
	req.Header.Set("X-Ops-Signature", alerts.Sign(cfg.AlertSigningSecret, timestamp, body))

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", response.Code)
	}
}

func TestAlertIncidentWriteIsTimeBoundedAndFailsClosed(t *testing.T) {
	cfg, err := config.Load(map[string]string{
		"OPS_ALERT_SIGNING_SECRET":       "0123456789abcdef0123456789abcdef",
		"OPS_INCIDENT_OPERATION_TIMEOUT": "10ms",
	})
	if err != nil {
		t.Fatal(err)
	}
	sink := alerts.SinkFunc(func(ctx context.Context, _ alerts.Accepted) error {
		<-ctx.Done()
		return ctx.Err()
	})
	server := NewWithAlertSink(cfg, nil, nil, sink)
	body := []byte(`{"version":"v1","alert_id":"alert-timeout","source":"prometheus","name":"api_latency","service":"api","environment":"staging","severity":"critical","starts_at":"2026-08-14T10:00:00Z","summary":"API latency is above its SLO."}`)
	timestamp := time.Now().UTC().Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodPost, "/v1/alerts", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ops-Timestamp", timestamp)
	req.Header.Set("X-Ops-Delivery-ID", "delivery-timeout")
	req.Header.Set("X-Ops-Signature", alerts.Sign(cfg.AlertSigningSecret, timestamp, body))

	response := httptest.NewRecorder()
	started := time.Now()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", response.Code)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("sink timeout took %s", elapsed)
	}
}

func TestHealthReadinessAndMetrics(t *testing.T) {
	server, metrics := testServer(t)

	health := httptest.NewRecorder()
	server.Handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), "healthy") {
		t.Fatalf("health = %d %s", health.Code, health.Body.String())
	}

	notReady := httptest.NewRecorder()
	server.Handler().ServeHTTP(notReady, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if notReady.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness before serving = %d, want 503", notReady.Code)
	}

	server.serving.Store(true)
	metrics.SetReady(true)
	ready := httptest.NewRecorder()
	server.Handler().ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK || !strings.Contains(ready.Body.String(), `"autonomy_mode":"observe"`) {
		t.Fatalf("readiness = %d %s", ready.Code, ready.Body.String())
	}

	prom := httptest.NewRecorder()
	server.Handler().ServeHTTP(prom, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if prom.Code != http.StatusOK || !strings.Contains(prom.Body.String(), "ops_controller_ready 1") {
		t.Fatalf("metrics = %d %s", prom.Code, prom.Body.String())
	}
}

func TestServeShutsDownCleanlyAndDropsReadiness(t *testing.T) {
	server, _ := testServer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- server.Serve(ctx, listener) }()

	deadline := time.Now().Add(time.Second)
	for !server.Ready() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !server.Ready() {
		t.Fatal("server never became ready")
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not shut down within the configured bound")
	}
	if server.Ready() {
		t.Fatal("server remained ready after shutdown")
	}
}

func TestUnknownPathsUseBoundedMetricLabel(t *testing.T) {
	server, _ := testServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/incidents/attacker-controlled", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
	prom := httptest.NewRecorder()
	server.Handler().ServeHTTP(prom, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if strings.Contains(prom.Body.String(), "attacker-controlled") || !strings.Contains(prom.Body.String(), `route="not_found"`) {
		t.Fatalf("unbounded path label exposed:\n%s", prom.Body.String())
	}
}
