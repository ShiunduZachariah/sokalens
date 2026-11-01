package apifootball

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientGetSendsAPIKeyAndCapturesRateLimitHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/countries" {
			t.Fatalf("path = %q, want /countries", r.URL.Path)
		}
		if got := r.Header.Get("x-apisports-key"); got != "test-key" {
			t.Fatalf("x-apisports-key = %q, want test-key", got)
		}
		w.Header().Set("x-ratelimit-requests-limit", "100")
		w.Header().Set("x-ratelimit-requests-remaining", "99")
		w.Header().Set("X-RateLimit-Limit", "10")
		w.Header().Set("X-RateLimit-Remaining", "9")
		_, _ = w.Write([]byte(`{"response":[]}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:    server.URL,
		APIKey:     "test-key",
		HTTPClient: server.Client(),
	})

	resp, err := client.Get(context.Background(), "countries", nil)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if resp.Provider != ProviderName {
		t.Fatalf("Provider = %q, want %q", resp.Provider, ProviderName)
	}
	if resp.Endpoint != "countries" {
		t.Fatalf("Endpoint = %q, want countries", resp.Endpoint)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if resp.RateLimit.DailyLimit != 100 || resp.RateLimit.DailyRemaining != 99 {
		t.Fatalf("daily rate headers = %+v, want limit 100 remaining 99", resp.RateLimit)
	}
	if resp.RateLimit.MinuteLimit != 10 || resp.RateLimit.MinuteRemaining != 9 {
		t.Fatalf("minute rate headers = %+v, want limit 10 remaining 9", resp.RateLimit)
	}
	if !strings.Contains(string(resp.Body), `"response"`) {
		t.Fatalf("body = %s, want response envelope", resp.Body)
	}
}

func TestClientGetReturnsStatusErrorWithResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-requests-remaining", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"errors":{"rate":"too many requests"}}`))
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL, HTTPClient: server.Client()})

	resp, err := client.Get(context.Background(), "fixtures", nil)
	if !errors.Is(err, ErrProviderStatus) {
		t.Fatalf("error = %v, want ErrProviderStatus", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("StatusCode = %d, want 429", resp.StatusCode)
	}
	if resp.RateLimit.DailyRemaining != 0 {
		t.Fatalf("DailyRemaining = %d, want 0", resp.RateLimit.DailyRemaining)
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Category != "rate" || providerErr.Message != "too many requests" {
		t.Fatalf("error = %v, want typed rate provider error", err)
	}
}

func TestClientReturnsTypedErrorForSuccessfulHTTPProviderErrorEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errors":{"plan":"Free plans do not have access to the Last parameter."},"response":[]}`))
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL, HTTPClient: server.Client()})
	resp, err := client.Get(context.Background(), "fixtures", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %v, want typed ProviderError", err)
	}
	if providerErr.Category != "plan" || providerErr.Message != "Free plans do not have access to the Last parameter." {
		t.Fatalf("provider error = %+v", providerErr)
	}
}

func TestClientPausesAfterRateLimitInSuccessfulHTTPEnvelope(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"errors":{"rateLimit":"Too many requests. You have exceeded the limit of requests per minute of your subscription."},"response":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"errors":[],"response":[]}`))
	}))
	defer server.Close()

	now := time.Date(2026, 9, 15, 20, 0, 0, 0, time.UTC)
	client := NewClient(Config{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Now:        func() time.Time { return now },
	})
	_, firstErr := client.Get(context.Background(), "fixtures", nil)
	var providerErr *ProviderError
	// The property this test exists for is the pause itself -- asserted below
	// by calls staying at 1 -- not its length. The length is now proportionate:
	// a single rejection while the observed minute allowance is still healthy
	// backs off briefly, because pausing every caller for a minute over one
	// refused request cost production 4,300 blocked requests a minute against
	// 10 sent, and left 57,000 of 75,000 daily requests unspent.
	if !errors.As(firstErr, &providerErr) || !providerErr.Deferrable() {
		t.Fatalf("first error = %#v / %v, want a deferrable provider error", providerErr, firstErr)
	}
	if providerErr.RetryAfter() != shortRateLimitBackoff {
		t.Fatalf("retry after = %s, want the short backoff while headroom is healthy", providerErr.RetryAfter())
	}
	if _, secondErr := client.Get(context.Background(), "fixtures", nil); !errors.Is(secondErr, ErrRateLimitDeferred) {
		t.Fatalf("second Get error = %v, want ErrRateLimitDeferred", secondErr)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want body rate limit to pause before another HTTP request", calls.Load())
	}
	now = now.Add(time.Minute)
	if _, err := client.Get(context.Background(), "fixtures", nil); err != nil {
		t.Fatalf("Get after minute reset returned error: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls after reset = %d, want 2", calls.Load())
	}
}

func TestClientDefersAtMinuteReserveWithoutBlockingNextRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := calls.Add(1)
		w.Header().Set("x-ratelimit-requests-limit", "100")
		w.Header().Set("x-ratelimit-requests-remaining", "80")
		w.Header().Set("X-RateLimit-Limit", "10")
		if call == 1 {
			w.Header().Set("X-RateLimit-Remaining", "2")
		} else {
			w.Header().Set("X-RateLimit-Remaining", "9")
		}
		_, _ = w.Write([]byte(`{"errors":[],"response":[]}`))
	}))
	defer server.Close()

	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	client := NewClient(Config{
		BaseURL:       server.URL,
		HTTPClient:    server.Client(),
		DailyReserve:  20,
		MinuteReserve: 2,
		Now:           func() time.Time { return now },
	})
	if _, err := client.Get(context.Background(), "fixtures", nil); err != nil {
		t.Fatalf("first Get returned error: %v", err)
	}
	_, err := client.Get(context.Background(), "fixtures", nil)
	if !errors.Is(err, ErrRateLimitDeferred) {
		t.Fatalf("second Get error = %v, want ErrRateLimitDeferred", err)
	}
	var directive interface {
		Retryable() bool
		RetryAfter() time.Duration
	}
	if !errors.As(err, &directive) || !directive.Retryable() || directive.RetryAfter() != time.Minute {
		t.Fatalf("defer directive = %#v / %v, want retryable after one minute", directive, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want second request deferred before HTTP", calls.Load())
	}
	now = now.Add(time.Minute)
	if _, err := client.Get(context.Background(), "fixtures", nil); err != nil {
		t.Fatalf("Get after minute reset returned error: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls after reset = %d, want 2", calls.Load())
	}
}

func TestClientDefersAtMinuteReserveFromAPISportsHeaders(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("x-ratelimit-requests-limit", "75000")
		w.Header().Set("x-ratelimit-requests-remaining", "60000")
		w.Header().Set("x-ratelimit-minute-limit", "450")
		w.Header().Set("x-ratelimit-minute-remaining", "15")
		_, _ = w.Write([]byte(`{"errors":[],"response":[]}`))
	}))
	defer server.Close()

	now := time.Date(2026, 9, 15, 20, 0, 0, 0, time.UTC)
	client := NewClient(Config{BaseURL: server.URL, HTTPClient: server.Client(), DailyLimit: 75000, DailyReserve: 1500, MinuteLimit: 450, MinuteReserve: 15, Now: func() time.Time { return now }})
	if _, err := client.Get(context.Background(), "fixtures", nil); err != nil {
		t.Fatalf("first Get returned error: %v", err)
	}
	if _, err := client.Get(context.Background(), "fixtures", nil); !errors.Is(err, ErrRateLimitDeferred) {
		t.Fatalf("second Get error=%v, want ErrRateLimitDeferred", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d, want second request deferred before HTTP", calls.Load())
	}
}

func TestClientBlocksBeforeSpendingDailyReserve(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("x-ratelimit-requests-limit", "100")
		w.Header().Set("x-ratelimit-requests-remaining", "20")
		w.Header().Set("X-RateLimit-Limit", "10")
		w.Header().Set("X-RateLimit-Remaining", "9")
		_, _ = w.Write([]byte(`{"errors":[],"response":[]}`))
	}))
	defer server.Close()

	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	client := NewClient(Config{
		BaseURL:       server.URL,
		HTTPClient:    server.Client(),
		DailyReserve:  20,
		MinuteReserve: 2,
		Now:           func() time.Time { return now },
	})
	if _, err := client.Get(context.Background(), "fixtures", nil); err != nil {
		t.Fatalf("first Get returned error: %v", err)
	}
	_, err := client.Get(context.Background(), "fixtures", nil)
	if !errors.Is(err, ErrDailyQuotaReserved) {
		t.Fatalf("second Get error = %v, want ErrDailyQuotaReserved", err)
	}
	var directive interface {
		Retryable() bool
		RetryAfter() time.Duration
	}
	if !errors.As(err, &directive) || !directive.Retryable() || directive.RetryAfter() != time.Minute {
		t.Fatalf("reserve directive = %#v / %v, want retryable after 1m", directive, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want reserve to block second provider request", calls.Load())
	}
	now = time.Date(2026, 7, 14, 23, 59, 0, 0, time.UTC)
	if _, err := client.Get(context.Background(), "fixtures", nil); err != nil {
		t.Fatalf("late-day Get returned error after reserve decayed: %v", err)
	}
}

func TestClientEnforcesConfiguredDailyLimitFromPersistedSpend(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"errors":[],"response":[]}`))
	}))
	defer server.Close()

	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	client := NewClient(Config{
		BaseURL:           server.URL,
		HTTPClient:        server.Client(),
		DailyLimit:        5,
		DailyReserve:      2,
		InitialDailySpent: 2,
		Now:               func() time.Time { return now },
	})
	if _, err := client.Get(context.Background(), "fixtures", nil); err != nil {
		t.Fatalf("first Get returned error: %v", err)
	}
	if _, err := client.Get(context.Background(), "fixtures", nil); !errors.Is(err, ErrDailyQuotaReserved) {
		t.Fatalf("second Get error = %v, want local ErrDailyQuotaReserved", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want one request before configured reserve", calls.Load())
	}

	now = now.Add(24 * time.Hour)
	if _, err := client.Get(context.Background(), "fixtures", nil); err != nil {
		t.Fatalf("Get after UTC day reset returned error: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls after reset = %d, want 2", calls.Load())
	}
}

func TestClientHonorsRetryAfterFollowingRateLimit(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errors":{"rate":"slow down"},"response":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"errors":[],"response":[]}`))
	}))
	defer server.Close()

	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	client := NewClient(Config{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Now:        func() time.Time { return now },
	})
	_, firstErr := client.Get(context.Background(), "fixtures", nil)
	if !errors.Is(firstErr, ErrProviderStatus) {
		t.Fatalf("first Get error = %v, want status error", firstErr)
	}
	var directive interface {
		Retryable() bool
		RetryAfter() time.Duration
	}
	if !errors.As(firstErr, &directive) || !directive.Retryable() || directive.RetryAfter() != 3*time.Second {
		t.Fatalf("rate directive = %#v / %v, want retryable after 3s", directive, firstErr)
	}
	_, secondErr := client.Get(context.Background(), "fixtures", nil)
	if !errors.Is(secondErr, ErrRateLimitDeferred) {
		t.Fatalf("second Get error = %v, want ErrRateLimitDeferred", secondErr)
	}
	if !errors.As(secondErr, &directive) || !directive.Retryable() || directive.RetryAfter() != 3*time.Second {
		t.Fatalf("deferred directive = %#v / %v, want retryable after 3s", directive, secondErr)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want Retry-After to defer before HTTP", calls.Load())
	}
	now = now.Add(3 * time.Second)
	if _, err := client.Get(context.Background(), "fixtures", nil); err != nil {
		t.Fatalf("Get after Retry-After returned error: %v", err)
	}
}

func TestClientClassifiesUnauthorizedStatusAsPermanentProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL, HTTPClient: server.Client()})
	_, err := client.Get(context.Background(), "fixtures", nil)
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %v, want typed ProviderError", err)
	}
	if providerErr.Category != "auth" || providerErr.Retryable() {
		t.Fatalf("provider error = %+v, want permanent auth error", providerErr)
	}
}

func TestClientStillPausesAFullMinuteWhenTheAllowanceIsActuallySpent(t *testing.T) {
	// The other half of the proportionate backoff, and the case #160 added the
	// pause for. When the provider's own headers say the minute is gone, a
	// short retry would simply be refused again, so the full minute stands.
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("x-ratelimit-limit", "450")
		w.Header().Set("x-ratelimit-remaining", "0")
		_, _ = w.Write([]byte(`{"errors":{"rateLimit":"Too many requests."},"response":[]}`))
	}))
	defer server.Close()

	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	client := NewClient(Config{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Now:        func() time.Time { return now },
	})
	_, err := client.Get(context.Background(), "fixtures", nil)
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %v, want a provider error", err)
	}
	if providerErr.RetryAfter() != time.Minute {
		t.Fatalf("retry after = %s, want a full minute when the allowance is spent", providerErr.RetryAfter())
	}
}
