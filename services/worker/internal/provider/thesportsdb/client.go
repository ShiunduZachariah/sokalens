// Package thesportsdb is a polite, free-tier HTTP client for TheSportsDB
// (https://www.thesportsdb.com). The API key lives in the URL path — not a
// header — and the free tier exposes no per-minute quota headers, so the client
// enforces a simple one-request-per-second throttle instead of a quota
// governor. Logo/metadata backfills therefore stay well inside free-tier
// limits without any rate-limit bookkeeping.
package thesportsdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
)

// ProviderName is the worker registry identity for TheSportsDB.
const ProviderName = "thesportsdb"

// DefaultBaseURL embeds the free test key (123) in the URL path, which is how
// TheSportsDB authenticates requests.
const DefaultBaseURL = "https://www.thesportsdb.com/api/v1/json/123/"

// DefaultAPIKey is the free-tier test key.
const DefaultAPIKey = "123"

// MinRequestInterval is the polite one-request-per-second throttle.
const MinRequestInterval = time.Second

// ErrProviderStatus is returned when TheSportsDB answers with a non-2xx status.
var ErrProviderStatus = errors.New("provider returned non-success status")

// Config builds a TheSportsDB client. Zero values fall back to the free-tier
// defaults (base URL with the test key 123 embedded in the path).
type Config struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	Now        func() time.Time
	Sleep      func(context.Context, time.Duration) error
}

// Client issues GET requests against TheSportsDB with a simple polite
// one-request-per-second throttle. TheSportsDB sends no rate-limit headers, so
// responses carry a zero-value provider.RateLimit.
type Client struct {
	baseURL    string
	httpClient *http.Client
	now        func() time.Time
	sleep      func(context.Context, time.Duration) error

	mu          sync.Mutex
	lastRequest time.Time
}

// NewClient builds a TheSportsDB client with the free-tier defaults unless
// overridden.
func NewClient(cfg Config) *Client {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	sleep := cfg.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	return &Client{
		baseURL:    resolveBaseURL(cfg.BaseURL, cfg.APIKey),
		httpClient: httpClient,
		now:        now,
		sleep:      sleep,
	}
}

// resolveBaseURL normalizes the base URL and swaps the key segment after /json/
// when a non-default key is configured (TheSportsDB keys live in the URL path,
// not a header).
func resolveBaseURL(baseURL string, apiKey string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		apiKey = DefaultAPIKey
	}
	const marker = "/json/"
	if index := strings.Index(baseURL, marker); index >= 0 {
		rest := baseURL[index+len(marker):]
		end := strings.IndexByte(rest, '/')
		if end < 0 {
			end = len(rest)
		}
		if end > 0 {
			baseURL = baseURL[:index+len(marker)] + apiKey + rest[end:]
		}
	}
	return strings.TrimRight(baseURL, "/")
}

// Get performs a polite GET against {base}/{endpoint}?query and returns the
// raw response envelope. No special headers are set and RateLimit stays at its
// zero value (TheSportsDB has no quota headers on the free tier).
func (c *Client) Get(ctx context.Context, endpoint string, query url.Values) (provider.Response, error) {
	if err := c.waitForThrottle(ctx); err != nil {
		return provider.Response{}, err
	}
	endpoint = strings.Trim(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return provider.Response{}, errors.New("provider endpoint is required")
	}
	if c.baseURL == "" {
		return provider.Response{}, errors.New("provider base URL is required")
	}
	requestURL := c.baseURL + "/" + endpoint
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return provider.Response{}, err
	}

	start := c.now()
	httpResp, err := c.httpClient.Do(req)
	if err != nil {
		return provider.Response{}, err
	}
	defer httpResp.Body.Close()

	body, readErr := io.ReadAll(httpResp.Body)
	resp := provider.Response{
		Provider:      ProviderName,
		Endpoint:      endpoint,
		StatusCode:    httpResp.StatusCode,
		Body:          body,
		Latency:       c.now().Sub(start),
		ResponseBytes: len(body),
	}
	if readErr != nil {
		return resp, readErr
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return resp, fmt.Errorf("%w: %d", ErrProviderStatus, httpResp.StatusCode)
	}
	return resp, nil
}

// waitForThrottle sleeps until at least MinRequestInterval has passed since the
// previous request. The mutex is held across the sleep so concurrent callers
// are serialized into at most one request per second.
func (c *Client) waitForThrottle(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delay := c.lastRequest.Add(MinRequestInterval).Sub(c.now())
	if delay > 0 {
		if err := c.sleep(ctx, delay); err != nil {
			return err
		}
	}
	c.lastRequest = c.now()
	return nil
}
