package thesportsdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
)

func TestClientBuildsKeyInPathAndSendsNoAuthHeader(t *testing.T) {
	var gotPath string
	var gotAuthHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthHeader = r.Header.Get("X-Auth-Token")
		if r.URL.Query().Get("t") != "Arsenal" {
			t.Fatalf("query t = %q, want Arsenal", r.URL.Query().Get("t"))
		}
		_, _ = w.Write([]byte(`{"teams":null}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:    server.URL + "/api/v1/json/123",
		HTTPClient: server.Client(),
	})

	resp, err := client.Get(context.Background(), "searchteams.php", url.Values{"t": {"Arsenal"}})
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if gotPath != "/api/v1/json/123/searchteams.php" {
		t.Fatalf("path = %q, want key 123 embedded in URL path", gotPath)
	}
	if gotAuthHeader != "" {
		t.Fatalf("X-Auth-Token = %q, want no auth header (key lives in path)", gotAuthHeader)
	}
	if resp.Provider != ProviderName {
		t.Fatalf("Provider = %q, want %q", resp.Provider, ProviderName)
	}
	if resp.Endpoint != "searchteams.php" {
		t.Fatalf("Endpoint = %q, want searchteams.php", resp.Endpoint)
	}
	if !reflect.DeepEqual(resp.RateLimit, provider.RateLimitHeaders{}) {
		t.Fatalf("RateLimit = %+v, want zero value (free tier has no quota headers)", resp.RateLimit)
	}
}

func TestClientSwapsAPIKeySegmentInBaseURL(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"leagues":[]}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:    server.URL + "/api/v1/json/123/",
		APIKey:     "456789",
		HTTPClient: server.Client(),
	})
	if _, err := client.Get(context.Background(), "all_leagues.php", url.Values{}); err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if gotPath != "/api/v1/json/456789/all_leagues.php" {
		t.Fatalf("path = %q, want configured key 456789 in path", gotPath)
	}
}

func TestClientThrottlesToAtMostOneRequestPerSecond(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"teams":null}`))
	}))
	defer server.Close()

	var slept time.Duration
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	client := NewClient(Config{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Now: func() time.Time {
			return now
		},
		Sleep: func(_ context.Context, d time.Duration) error {
			slept += d
			now = now.Add(d)
			return nil
		},
	})

	if _, err := client.Get(context.Background(), "searchteams.php", nil); err != nil {
		t.Fatalf("first Get returned error: %v", err)
	}
	if _, err := client.Get(context.Background(), "searchteams.php", nil); err != nil {
		t.Fatalf("second Get returned error: %v", err)
	}
	if slept != time.Second {
		t.Fatalf("slept = %s, want 1s polite throttle", slept)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestClientSurfacesNonSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"teams":null}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Sleep:      func(context.Context, time.Duration) error { return nil },
	})
	resp, err := client.Get(context.Background(), "searchteams.php", nil)
	if err == nil {
		t.Fatal("Get returned nil error for 429")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("error = %v, want status code", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("StatusCode = %d, want 429", resp.StatusCode)
	}
}
