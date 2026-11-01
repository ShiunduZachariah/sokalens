package aicontract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func deepSeekResponseJSON(content string) string {
	encoded, _ := json.Marshal(content)
	return fmt.Sprintf(`{"id":"req-1","model":"deepseek-v4-flash","choices":[{"message":{"role":"assistant","content":%s}}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`, encoded)
}

func deepSeekRecommendationJSON() string {
	data, _ := json.Marshal(recommendation())
	return string(data)
}

func testDeepSeekProvider(t *testing.T, handler http.HandlerFunc, mutate func(*DeepSeekConfig)) (*DeepSeekProvider, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	config := DeepSeekConfig{APIKey: "test-secret", Endpoint: server.URL + "/chat/completions", MaxRetries: 0}
	if mutate != nil {
		mutate(&config)
	}
	provider, err := newDeepSeekProvider(config, server.Client(), true)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return provider, server
}

func TestDeepSeekValidResponseAndUsage(t *testing.T) {
	provider, server := testDeepSeekProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("authorization header missing")
		}
		var request deepSeekRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != DeepSeekDefaultModel || request.ResponseFormat.Type != "json_object" || !strings.Contains(request.Messages[0].Content, "JSON") {
			t.Errorf("unexpected request: %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(deepSeekResponseJSON(deepSeekRecommendationJSON())))
	}, func(config *DeepSeekConfig) {
		config.Pricing = DeepSeekPricing{InputMicrosPerMillionTokens: 1_000_000, OutputMicrosPerMillionTokens: 2_000_000}
	})
	defer server.Close()

	result, err := provider.RecommendWithMetadata(context.Background(), evidence())
	if err != nil {
		t.Fatal(err)
	}
	if result.Recommendation.IncidentID != "inc-1" || result.Usage.TotalTokens != 120 || result.Usage.CostMicros != 140 || result.Usage.Attempts != 1 || result.RequestID != "req-1" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestDeepSeekRejectsMissingOrOversizeResponseMetadata(t *testing.T) {
	valid := deepSeekResponseJSON(deepSeekRecommendationJSON())
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "missing request id", body: strings.Replace(valid, `"id":"req-1"`, `"id":""`, 1)},
		{name: "oversize request id", body: strings.Replace(valid, `"id":"req-1"`, `"id":"`+strings.Repeat("x", 257)+`"`, 1)},
		{name: "missing model", body: strings.Replace(valid, `"model":"deepseek-v4-flash"`, `"model":""`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider, server := testDeepSeekProvider(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(test.body)) }, nil)
			defer server.Close()
			_, err := provider.RecommendWithMetadata(context.Background(), evidence())
			var providerErr *DeepSeekError
			if !errors.As(err, &providerErr) || providerErr.Stage != "response_envelope" {
				t.Fatalf("error = %v, want bounded response metadata rejection", err)
			}
		})
	}
}

func TestDeepSeekClassifiesPaymentAndRateLimitWithoutLeakingKey(t *testing.T) {
	tests := []struct {
		status int
		kind   DeepSeekErrorKind
	}{
		{http.StatusPaymentRequired, DeepSeekErrorPaymentRequired},
		{http.StatusTooManyRequests, DeepSeekErrorRateLimited},
	}
	for _, test := range tests {
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			provider, server := testDeepSeekProvider(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(`{"error":"test-secret provider detail"}`))
			}, nil)
			defer server.Close()
			_, err := provider.Recommend(context.Background(), evidence())
			var providerErr *DeepSeekError
			if !errors.As(err, &providerErr) || providerErr.Kind != test.kind || providerErr.RetryAfter != 7*time.Second {
				t.Fatalf("unexpected error: %#v", err)
			}
			if strings.Contains(err.Error(), "test-secret") {
				t.Fatal("API key leaked in error")
			}
		})
	}
}

func TestDeepSeekRetryIsBoundedAndHonorsRetryAfter(t *testing.T) {
	var calls atomic.Int32
	provider, server := testDeepSeekProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(deepSeekResponseJSON(deepSeekRecommendationJSON())))
	}, func(config *DeepSeekConfig) { config.MaxRetries = 1 })
	defer server.Close()
	var slept time.Duration
	provider.sleep = func(_ context.Context, delay time.Duration) error { slept = delay; return nil }
	result, err := provider.RecommendWithMetadata(context.Background(), evidence())
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || slept != time.Second || result.Usage.Attempts != 2 || !result.Usage.Complete || result.Usage.TotalTokens != 120 {
		t.Fatalf("calls=%d slept=%s usage=%+v", calls.Load(), slept, result.Usage)
	}
}

func TestDeepSeekDoesNotRetryLongRetryAfter(t *testing.T) {
	var calls atomic.Int32
	provider, server := testDeepSeekProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}, func(config *DeepSeekConfig) { config.MaxRetries = 2 })
	defer server.Close()
	_, err := provider.Recommend(context.Background(), evidence())
	if err == nil || calls.Load() != 1 {
		t.Fatalf("expected one bounded attempt, calls=%d err=%v", calls.Load(), err)
	}
}

func TestDeepSeekTimeoutAndCancellation(t *testing.T) {
	provider, server := testDeepSeekProvider(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, func(config *DeepSeekConfig) { config.Timeout = time.Second })
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := provider.Recommend(ctx, evidence())
	var providerErr *DeepSeekError
	if !errors.As(err, &providerErr) || providerErr.Kind != DeepSeekErrorCanceled {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestDeepSeekDeadlineIsClassifiedAsTimeout(t *testing.T) {
	provider, server := testDeepSeekProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		_, _ = w.Write([]byte(deepSeekResponseJSON(deepSeekRecommendationJSON())))
	}, func(config *DeepSeekConfig) { config.Timeout = time.Second })
	defer server.Close()
	_, err := provider.Recommend(context.Background(), evidence())
	var providerErr *DeepSeekError
	if !errors.As(err, &providerErr) || providerErr.Kind != DeepSeekErrorTimeout {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestDeepSeekRejectsOversizeMarkdownAndInvalidSchema(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		limit int64
	}{
		{"oversize", strings.Repeat("x", 2048), 1024},
		{"markdown", deepSeekResponseJSON("```json\n" + deepSeekRecommendationJSON() + "\n```"), 64 << 10},
		{"invalid schema", deepSeekResponseJSON(`{"schema_version":"1","incident_id":"inc-1","classification":"x","confidence":2,"actions":[]}`), 64 << 10},
		{"unknown field", deepSeekResponseJSON(strings.TrimSuffix(deepSeekRecommendationJSON(), "}") + `,"extra":true}`), 64 << 10},
		{"missing usage", fmt.Sprintf(`{"id":"req-1","model":"deepseek-v4-flash","choices":[{"message":{"role":"assistant","content":%q}}]}`, deepSeekRecommendationJSON()), 64 << 10},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, server := testDeepSeekProvider(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(test.body)) }, func(config *DeepSeekConfig) { config.MaxResponseBytes = test.limit })
			defer server.Close()
			_, err := provider.Recommend(context.Background(), evidence())
			var providerErr *DeepSeekError
			if !errors.As(err, &providerErr) || providerErr.Kind != DeepSeekErrorInvalidOutput {
				t.Fatalf("expected invalid output, got %v", err)
			}
		})
	}
}

func TestDeepSeekRejectsOversizeRequestBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	provider, server := testDeepSeekProvider(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) }, func(config *DeepSeekConfig) { config.MaxRequestBytes = 1024 })
	defer server.Close()
	input := evidence()
	input.References = []EvidenceReference{{Kind: "trace", ID: strings.Repeat("x", 2_000)}}
	_, err := provider.Recommend(context.Background(), input)
	var providerErr *DeepSeekError
	if !errors.As(err, &providerErr) || providerErr.Kind != DeepSeekErrorConfiguration || providerErr.Stage != "request_size" || calls.Load() != 0 {
		t.Fatalf("expected pre-network request rejection, calls=%d err=%v", calls.Load(), err)
	}
}

func TestDeepSeekProductionClientRefusesRedirects(t *testing.T) {
	provider, err := NewDeepSeekProvider(DeepSeekConfig{APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://other.example", nil)
	if provider.client.CheckRedirect == nil || provider.client.CheckRedirect(request, nil) == nil {
		t.Fatal("production client accepted a redirect")
	}
}

func TestDeepSeekEmptyJSONModeOutputRetriesOnlyWithinBound(t *testing.T) {
	var calls atomic.Int32
	provider, server := testDeepSeekProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(deepSeekResponseJSON("")))
	}, func(config *DeepSeekConfig) { config.MaxRetries = 1 })
	defer server.Close()
	provider.sleep = func(context.Context, time.Duration) error { return nil }
	_, err := provider.Recommend(context.Background(), evidence())
	if err == nil || calls.Load() != 2 {
		t.Fatalf("expected two bounded attempts, calls=%d err=%v", calls.Load(), err)
	}
}

func TestDeepSeekCircuitBreakerAndRecovery(t *testing.T) {
	var calls atomic.Int32
	provider, server := testDeepSeekProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}, func(config *DeepSeekConfig) {
		config.CircuitFailureThreshold = 2
		config.CircuitOpenDuration = time.Second
	})
	defer server.Close()
	now := time.Unix(100, 0)
	provider.now = func() time.Time { return now }
	for range 2 {
		_, _ = provider.Recommend(context.Background(), evidence())
	}
	_, err := provider.Recommend(context.Background(), evidence())
	var providerErr *DeepSeekError
	if !errors.As(err, &providerErr) || providerErr.Kind != DeepSeekErrorCircuitOpen || calls.Load() != 2 {
		t.Fatalf("circuit did not open: calls=%d err=%v", calls.Load(), err)
	}
	now = now.Add(2 * time.Second)
	_, _ = provider.Recommend(context.Background(), evidence())
	if calls.Load() != 3 {
		t.Fatalf("circuit did not recover, calls=%d", calls.Load())
	}
}

func TestDeepSeekConcurrencyLimitFailsFast(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider, server := testDeepSeekProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(deepSeekResponseJSON(deepSeekRecommendationJSON())))
	}, func(config *DeepSeekConfig) { config.MaxConcurrency = 1 })
	defer server.Close()
	done := make(chan error, 1)
	go func() { _, err := provider.Recommend(context.Background(), evidence()); done <- err }()
	<-started
	_, err := provider.Recommend(context.Background(), evidence())
	var providerErr *DeepSeekError
	if !errors.As(err, &providerErr) || providerErr.Kind != DeepSeekErrorConcurrency {
		t.Fatalf("expected concurrency rejection, got %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDeepSeekProductionEndpointIsStrictlyAllowlisted(t *testing.T) {
	for _, endpoint := range []string{"http://api.deepseek.com/chat/completions", "https://evil.example/chat/completions", "https://api.deepseek.com:443/chat/completions", "https://api.deepseek.com/chat/completions?key=x"} {
		_, err := NewDeepSeekProvider(DeepSeekConfig{APIKey: "secret", Endpoint: endpoint})
		if err == nil {
			t.Fatalf("accepted endpoint %q", endpoint)
		}
	}
	if _, err := NewDeepSeekProvider(DeepSeekConfig{APIKey: "secret"}); err != nil {
		t.Fatal(err)
	}
}

func TestDeepSeekConfigurationErrorsNeverExposeAPIKey(t *testing.T) {
	key := "super-secret-value"
	_, err := NewDeepSeekProvider(DeepSeekConfig{APIKey: key, Endpoint: "http://bad.example/path"})
	if err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("unsafe configuration error: %v", err)
	}
}

func TestDeepSeekConfigFromEnvironmentIsValidatedWithoutLeakingValues(t *testing.T) {
	values := map[string]string{
		"DEEPSEEK_API_KEY":                          "env-super-secret",
		"DEEPSEEK_MODEL":                            "deepseek-v4-pro",
		"DEEPSEEK_TIMEOUT":                          "9s",
		"DEEPSEEK_MAX_CONCURRENCY":                  "3",
		"DEEPSEEK_MAX_RETRIES":                      "1",
		"DEEPSEEK_MAX_RESPONSE_BYTES":               "32768",
		"DEEPSEEK_INPUT_MICROS_PER_MILLION_TOKENS":  "100",
		"DEEPSEEK_OUTPUT_MICROS_PER_MILLION_TOKENS": "200",
	}
	config, err := deepSeekConfigFromLookup(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if config.Model != "deepseek-v4-pro" || config.Timeout != 9*time.Second || config.MaxConcurrency != 3 || config.Pricing.OutputMicrosPerMillionTokens != 200 {
		t.Fatalf("unexpected environment config: %#v", config)
	}
	values["DEEPSEEK_TIMEOUT"] = "env-super-secret"
	_, err = deepSeekConfigFromLookup(func(name string) string { return values[name] })
	if err == nil || strings.Contains(err.Error(), "env-super-secret") {
		t.Fatalf("unsafe parse error: %v", err)
	}
}
