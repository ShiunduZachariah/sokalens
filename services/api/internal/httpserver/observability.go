package httpserver

import (
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/admin"
)

var defaultRequestMetrics = newRequestMetrics()

var latencyBuckets = [...]time.Duration{
	50 * time.Millisecond,
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	3 * time.Second,
	10 * time.Second,
}

type requestMetricKey struct {
	Method string
	Route  string
}

type requestMetric struct {
	Requests     uint64
	ClientErrors uint64
	ServerErrors uint64
	Duration     time.Duration
	Buckets      [len(latencyBuckets) + 1]uint64
	StatusClass  [5]uint64
}

type requestMetrics struct {
	mu      sync.RWMutex
	started time.Time
	values  map[requestMetricKey]requestMetric
}

type requestMetricSnapshot struct {
	Method                string  `json:"method"`
	Route                 string  `json:"route"`
	Requests              uint64  `json:"requests"`
	ClientErrors          uint64  `json:"client_errors"`
	ServerErrors          uint64  `json:"server_errors"`
	ErrorRate             float64 `json:"server_error_rate"`
	AverageMS             float64 `json:"average_ms"`
	ApproxP95MS           float64 `json:"approx_p95_ms"`
	RequestsUnder3Seconds uint64  `json:"requests_under_3_seconds"`
}

type diagnosticsSnapshot struct {
	StartedAt     time.Time               `json:"started_at"`
	UptimeSeconds int64                   `json:"uptime_seconds"`
	Requests      uint64                  `json:"requests"`
	ServerErrors  uint64                  `json:"server_errors"`
	ErrorRate     float64                 `json:"server_error_rate"`
	Goroutines    int                     `json:"goroutines"`
	HeapAlloc     uint64                  `json:"heap_alloc_bytes"`
	Routes        []requestMetricSnapshot `json:"routes"`
}

func newRequestMetrics() *requestMetrics {
	return &requestMetrics{started: time.Now().UTC(), values: make(map[requestMetricKey]requestMetric)}
}

func (m *requestMetrics) observe(method, path string, status int, duration time.Duration) {
	if m == nil {
		return
	}
	key := requestMetricKey{Method: strings.ToUpper(method), Route: metricRoute(path)}
	m.mu.Lock()
	value := m.values[key]
	value.Requests++
	value.Duration += duration
	if status >= 400 && status < 500 {
		value.ClientErrors++
	}
	if status >= 500 {
		value.ServerErrors++
	}
	statusClass := status / 100
	if statusClass >= 1 && statusClass <= 5 {
		value.StatusClass[statusClass-1]++
	}
	bucket := len(latencyBuckets)
	for index, upperBound := range latencyBuckets {
		if duration <= upperBound {
			bucket = index
			break
		}
	}
	value.Buckets[bucket]++
	m.values[key] = value
	m.mu.Unlock()
}

func (m *requestMetrics) snapshot(now time.Time) diagnosticsSnapshot {
	if m == nil {
		return diagnosticsSnapshot{}
	}
	m.mu.RLock()
	started := m.started
	values := make(map[requestMetricKey]requestMetric, len(m.values))
	for key, value := range m.values {
		values[key] = value
	}
	m.mu.RUnlock()

	result := diagnosticsSnapshot{StartedAt: started, UptimeSeconds: max(0, int64(now.Sub(started).Seconds())), Goroutines: runtime.NumGoroutine()}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	result.HeapAlloc = memory.HeapAlloc
	for key, value := range values {
		item := snapshotMetric(key, value)
		result.Requests += value.Requests
		result.ServerErrors += value.ServerErrors
		result.Routes = append(result.Routes, item)
	}
	if result.Requests > 0 {
		result.ErrorRate = float64(result.ServerErrors) / float64(result.Requests)
	}
	sort.Slice(result.Routes, func(i, j int) bool {
		if result.Routes[i].Route != result.Routes[j].Route {
			return result.Routes[i].Route < result.Routes[j].Route
		}
		return result.Routes[i].Method < result.Routes[j].Method
	})
	return result
}

func snapshotMetric(key requestMetricKey, value requestMetric) requestMetricSnapshot {
	result := requestMetricSnapshot{Method: key.Method, Route: key.Route, Requests: value.Requests, ClientErrors: value.ClientErrors, ServerErrors: value.ServerErrors}
	if value.Requests > 0 {
		result.ErrorRate = float64(value.ServerErrors) / float64(value.Requests)
		result.AverageMS = float64(value.Duration.Microseconds()) / 1000 / float64(value.Requests)
	}
	target := (value.Requests*95 + 99) / 100
	var cumulative uint64
	for index, count := range value.Buckets {
		cumulative += count
		if index <= 5 {
			result.RequestsUnder3Seconds = cumulative
		}
		if result.ApproxP95MS == 0 && cumulative >= target {
			if index < len(latencyBuckets) {
				result.ApproxP95MS = float64(latencyBuckets[index].Microseconds()) / 1000
			} else {
				result.ApproxP95MS = -1
			}
		}
	}
	return result
}

func observabilityMiddleware(metrics *requestMetrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		statusWriter := &statusResponseWriter{ResponseWriter: w}
		next.ServeHTTP(statusWriter, r)
		status := statusWriter.statusCode
		if status == 0 {
			status = http.StatusOK
		}
		metrics.observe(r.Method, r.URL.Path, status, time.Since(started))
	})
}

func observabilityMetricsHandler(auth AdminAuthenticator, metrics *requestMetrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionReadAnalytics); !ok {
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writePrometheus(w, metrics)
	}
}

func observabilityDiagnosticsHandler(auth AdminAuthenticator, metrics *requestMetrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionReadAnalytics); !ok {
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, metrics.snapshot(time.Now().UTC()))
	}
}

func writePrometheus(w io.Writer, metrics *requestMetrics) {
	snapshot := metrics.snapshot(time.Now().UTC())
	_, _ = io.WriteString(w, "# HELP sokalens_http_requests_total Completed HTTP requests.\n# TYPE sokalens_http_requests_total counter\n")
	_, _ = io.WriteString(w, "# HELP sokalens_http_server_errors_total Completed HTTP 5xx responses.\n# TYPE sokalens_http_server_errors_total counter\n")
	_, _ = io.WriteString(w, "# HELP sokalens_http_request_duration_seconds Request duration histogram.\n# TYPE sokalens_http_request_duration_seconds histogram\n")
	for _, route := range snapshot.Routes {
		labels := `method="` + metricEscape(route.Method) + `",route="` + metricEscape(route.Route) + `"`
		_, _ = fmt.Fprintf(w, "sokalens_http_requests_total{%s} %d\n", labels, route.Requests)
		_, _ = fmt.Fprintf(w, "sokalens_http_server_errors_total{%s} %d\n", labels, route.ServerErrors)
		metric := metrics.value(requestMetricKey{Method: route.Method, Route: route.Route})
		var cumulative uint64
		for index, count := range metric.Buckets {
			cumulative += count
			upperBound := "+Inf"
			if index < len(latencyBuckets) {
				upperBound = strconv.FormatFloat(latencyBuckets[index].Seconds(), 'f', -1, 64)
			}
			_, _ = fmt.Fprintf(w, "sokalens_http_request_duration_seconds_bucket{%s,le=\"%s\"} %d\n", labels, upperBound, cumulative)
		}
		_, _ = fmt.Fprintf(w, "sokalens_http_request_duration_seconds_sum{%s} %.6f\n", labels, metric.Duration.Seconds())
		_, _ = fmt.Fprintf(w, "sokalens_http_request_duration_seconds_count{%s} %d\n", labels, metric.Requests)
	}
}

func (m *requestMetrics) value(key requestMetricKey) requestMetric {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.values[key]
}

func metricRoute(path string) string {
	if _, known := fixedMetricRoutes[path]; known {
		return path
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 3 && parts[0] == "v1" && (parts[1] == "matches" || parts[1] == "predictions" || parts[1] == "analysis") && looksLikeID(parts[2]) {
		return "/v1/" + parts[1] + "/:id"
	}
	if len(parts) == 4 && parts[0] == "v1" && parts[1] == "me" && (parts[2] == "sessions" || parts[2] == "notifications") && looksLikeID(parts[3]) {
		return "/v1/me/" + parts[2] + "/:id"
	}
	if len(parts) >= 3 && parts[0] == "v1" && parts[1] == "admin" {
		return "/v1/admin/other"
	}
	return "other"
}

var fixedMetricRoutes = map[string]struct{}{
	"/health": {}, "/healthz": {}, "/ready": {}, "/readyz": {},
	"/v1/mobile/config": {}, "/v1/matches/today": {}, "/v1/matches/history": {},
	"/v1/predictions/today": {}, "/v1/predictions/hit-rate": {}, "/v1/best-bets": {},
	"/v1/access": {}, "/v1/access/context": {}, "/v1/auth/register": {}, "/v1/auth/login": {},
	"/v1/growth/events": {},
	"/v1/auth/logout":   {}, "/v1/me": {}, "/v1/me/profile": {}, "/v1/me/sessions": {},
	"/v1/me/favorites": {}, "/v1/me/interests": {}, "/v1/me/notifications": {},
	"/v1/me/subscription": {}, "/v1/plans": {}, "/v1/recommendations": {},
}

func looksLikeID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') && (character < 'A' || character > 'F') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func metricEscape(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return strings.ReplaceAll(value, "\n", `\n`)
}
