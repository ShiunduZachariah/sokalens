package footballdata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestClientUsesAuthTokenHeaderAndParsesThrottleHeaders(t *testing.T) {
	var gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Auth-Token")
		if r.URL.Path != "/v4/competitions" {
			t.Fatalf("path = %q, want /v4/competitions", r.URL.Path)
		}
		w.Header().Set("X-API-Version", "v4")
		w.Header().Set("X-Requests-Available-Minute", "7")
		w.Header().Set("X-RequestCounter-Reset", "12")
		_, _ = w.Write([]byte(`{"count":0,"competitions":[]}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:     server.URL + "/v4",
		APIKey:      "test-token",
		MinuteLimit: 10,
		HTTPClient:  server.Client(),
	})

	resp, err := client.Get(context.Background(), "competitions", url.Values{})
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if gotToken != "test-token" {
		t.Fatalf("X-Auth-Token = %q, want test-token", gotToken)
	}
	if resp.Provider != ProviderName {
		t.Fatalf("Provider = %q, want %q", resp.Provider, ProviderName)
	}
	if resp.RateLimit.MinuteLimit != 10 || resp.RateLimit.MinuteRemaining != 7 {
		t.Fatalf("minute rate limit = %+v, want limit=10 remaining=7", resp.RateLimit)
	}
	if resp.RateLimit.Raw["x-requestcounter-reset"] != "12" {
		t.Fatalf("raw reset header = %q, want 12", resp.RateLimit.Raw["x-requestcounter-reset"])
	}
	if resp.RateLimit.Raw["x-requests-available-minute"] != "7" {
		t.Fatalf("raw available header = %q, want 7", resp.RateLimit.Raw["x-requests-available-minute"])
	}
}

func TestClientBacksOffBeforeNextRequestWhenHeadersShowNoRequestsAvailable(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("X-RequestsAvailable", "0")
			w.Header().Set("X-RequestCounter-Reset", "3")
		} else {
			w.Header().Set("X-RequestsAvailable", "9")
			w.Header().Set("X-RequestCounter-Reset", "60")
		}
		_, _ = w.Write([]byte(`{"count":0,"competitions":[]}`))
	}))
	defer server.Close()

	var slept time.Duration
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	client := NewClient(Config{
		BaseURL:     server.URL,
		APIKey:      "test-token",
		MinuteLimit: 10,
		HTTPClient:  server.Client(),
		Now: func() time.Time {
			return now
		},
		Sleep: func(_ context.Context, d time.Duration) error {
			slept += d
			now = now.Add(d)
			return nil
		},
	})

	if _, err := client.Get(context.Background(), "competitions", nil); err != nil {
		t.Fatalf("first Get returned error: %v", err)
	}
	if _, err := client.Get(context.Background(), "competitions", nil); err != nil {
		t.Fatalf("second Get returned error: %v", err)
	}
	if slept != 3*time.Second {
		t.Fatalf("slept = %s, want 3s", slept)
	}
}
