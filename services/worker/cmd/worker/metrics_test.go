package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/metrics"
)

func TestBackfillQueueGaugeReportsZeroAfterQueueDrains(t *testing.T) {
	registry := metrics.New()
	publishQueueDepthGauges(registry, map[string]int{"data-backfill": 1200})
	publishQueueDepthGauges(registry, map[string]int{"predictions": 1})

	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	if !strings.Contains(body, "worker_jobs_queued_data_backfill 0\n") {
		t.Fatalf("drained backfill queue must remain observable as zero: %s", body)
	}
	if !strings.Contains(body, "worker_jobs_queued_predictions 1\n") {
		t.Fatalf("other queue depths must be preserved: %s", body)
	}
}

func TestBackfillQueueGaugeExistsWhenInitiallyEmpty(t *testing.T) {
	registry := metrics.New()
	publishQueueDepthGauges(registry, map[string]int{})
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), "worker_jobs_queued_data_backfill 0\n") {
		t.Fatalf("empty backfill queue metric missing: %s", response.Body.String())
	}
}
