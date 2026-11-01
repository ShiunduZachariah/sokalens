package footballdata

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/quota"
)

const ProviderName = "football-data-org"

var ErrProviderStatus = errors.New("provider returned non-success status")

type Config struct {
	BaseURL     string
	APIKey      string
	MinuteLimit int
	HTTPClient  *http.Client
	Now         func() time.Time
	Sleep       func(context.Context, time.Duration) error
}

type Client struct {
	baseURL     string
	apiKey      string
	minuteLimit int
	httpClient  *http.Client
	now         func() time.Time
	sleep       func(context.Context, time.Duration) error

	mu            sync.Mutex
	throttleUntil time.Time
	bucket        *quota.TokenBucket
}

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
	minuteLimit := cfg.MinuteLimit
	if minuteLimit <= 0 {
		minuteLimit = 10
	}
	return &Client{
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:      cfg.APIKey,
		minuteLimit: minuteLimit,
		httpClient:  httpClient,
		now:         now,
		sleep:       sleep,
		bucket:      quota.NewTokenBucket(minuteLimit, now),
	}
}

func (c *Client) Get(ctx context.Context, endpoint string, query url.Values) (provider.Response, error) {
	if err := c.waitForThrottle(ctx); err != nil {
		return provider.Response{}, err
	}
	// Pace concurrent free-provider backfills proactively. Retry-After alone is
	// too late: by then a burst has already consumed the minute allowance.
	if c.bucket != nil {
		if err := c.bucket.Wait(ctx); err != nil {
			return provider.Response{}, err
		}
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
	if c.apiKey != "" {
		req.Header.Set("X-Auth-Token", c.apiKey)
	}

	start := c.now()
	httpResp, err := c.httpClient.Do(req)
	if err != nil {
		return provider.Response{}, err
	}
	defer httpResp.Body.Close()

	body, readErr := io.ReadAll(httpResp.Body)
	rateLimit := parseRateLimitHeaders(httpResp.Header, c.minuteLimit)
	resp := provider.Response{
		Provider:      ProviderName,
		Endpoint:      endpoint,
		StatusCode:    httpResp.StatusCode,
		Body:          body,
		Latency:       c.now().Sub(start),
		ResponseBytes: len(body),
		RateLimit:     rateLimit,
	}
	c.updateThrottle(httpResp.StatusCode, httpResp.Header, rateLimit)
	if c.bucket != nil && rateLimit.MinuteRemaining > 0 && firstNonEmptyHeader(httpResp.Header, "X-RequestsAvailable", "X-Requests-Available-Minute") != "" {
		c.bucket.Observe(rateLimit.MinuteLimit, rateLimit.MinuteRemaining)
	}
	if readErr != nil {
		return resp, readErr
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return resp, fmt.Errorf("%w: %d", ErrProviderStatus, httpResp.StatusCode)
	}
	return resp, nil
}

func (c *Client) waitForThrottle(ctx context.Context) error {
	c.mu.Lock()
	until := c.throttleUntil
	c.mu.Unlock()
	if until.IsZero() {
		return nil
	}
	delay := until.Sub(c.now())
	if delay <= 0 {
		return nil
	}
	return c.sleep(ctx, delay)
}

func (c *Client) updateThrottle(status int, headers http.Header, rateLimit provider.RateLimitHeaders) {
	resetSeconds := parseHeaderInt(headers.Get("X-RequestCounter-Reset"))
	if retryAfter := parseHeaderInt(headers.Get("Retry-After")); retryAfter > resetSeconds {
		resetSeconds = retryAfter
	}
	if resetSeconds <= 0 {
		return
	}
	shouldThrottle := status == http.StatusTooManyRequests || (rateLimit.MinuteLimit > 0 && rateLimit.MinuteRemaining <= 0)
	if !shouldThrottle {
		return
	}
	until := c.now().Add(time.Duration(resetSeconds) * time.Second)
	c.mu.Lock()
	if until.After(c.throttleUntil) {
		c.throttleUntil = until
	}
	c.mu.Unlock()
}

func parseRateLimitHeaders(headers http.Header, minuteLimit int) provider.RateLimitHeaders {
	raw := map[string]string{}
	for _, key := range []string{
		"X-API-Version",
		"X-Authenticated-Client",
		"X-RequestCounter-Reset",
		"X-RequestsAvailable",
		"X-Requests-Available-Minute",
		"Retry-After",
	} {
		if value := headers.Get(key); value != "" {
			raw[strings.ToLower(key)] = value
		}
	}
	availableHeader := firstNonEmptyHeader(headers, "X-RequestsAvailable", "X-Requests-Available-Minute")
	minuteRemaining := parseHeaderInt(availableHeader)
	if minuteRemaining == 0 && availableHeader == "" {
		minuteLimit = 0
	}
	return provider.RateLimitHeaders{
		MinuteLimit:     minuteLimit,
		MinuteRemaining: minuteRemaining,
		Raw:             raw,
	}
}

func firstNonEmptyHeader(headers http.Header, keys ...string) string {
	for _, key := range keys {
		if value := headers.Get(key); value != "" {
			return value
		}
	}
	return ""
}

func parseHeaderInt(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return value
}
