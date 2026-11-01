package apifootball

import (
	"context"
	"encoding/json"
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

const ProviderName = "api-football"

var ErrProviderStatus = errors.New("provider returned non-success status")

var ErrDailyQuotaReserved = errors.New("daily provider quota reserve reached")

var ErrRateLimitDeferred = errors.New("provider request deferred by rate limit")

// shortRateLimitBackoff is how long a single rejected request pauses the
// client when the provider gave no Retry-After and the observed minute
// allowance is still healthy. Long enough to break a tight loop, short enough
// that a spurious rejection costs a few requests rather than a minute of
// them.
const shortRateLimitBackoff = 2 * time.Second

type dailyQuotaReservedError struct {
	retryAfter time.Duration
}

type rateLimitDeferredError struct {
	retryAfter time.Duration
}

func (e *rateLimitDeferredError) Error() string             { return ErrRateLimitDeferred.Error() }
func (e *rateLimitDeferredError) Unwrap() error             { return ErrRateLimitDeferred }
func (e *rateLimitDeferredError) Retryable() bool           { return true }
func (e *rateLimitDeferredError) RetryAfter() time.Duration { return e.retryAfter }
func (e *rateLimitDeferredError) Deferrable() bool          { return true }

func (e *dailyQuotaReservedError) Error() string             { return ErrDailyQuotaReserved.Error() }
func (e *dailyQuotaReservedError) Unwrap() error             { return ErrDailyQuotaReserved }
func (e *dailyQuotaReservedError) Retryable() bool           { return true }
func (e *dailyQuotaReservedError) RetryAfter() time.Duration { return e.retryAfter }
func (e *dailyQuotaReservedError) Deferrable() bool          { return true }

const (
	// The package we pay for. See services/worker/internal/config: this is a
	// fallback for a client built without configuration, and it is
	// deliberately the smaller plan. A fallback that guesses high spends
	// money that was never authorised; one that guesses low throttles, which
	// is visible and recoverable.
	defaultDailyLimit    = 7500
	defaultDailyReserve  = 600
	defaultMinuteLimit   = 450
	defaultMinuteReserve = 15
)

type Config struct {
	BaseURL           string
	APIKey            string
	DailyLimit        int
	DailyReserve      int
	MinuteLimit       int
	MinuteReserve     int
	InitialDailySpent int
	HTTPClient        *http.Client
	Now               func() time.Time
}

type Client struct {
	baseURL       string
	apiKey        string
	dailyLimit    int
	dailyReserve  int
	minuteLimit   int
	minuteReserve int
	httpClient    *http.Client
	now           func() time.Time

	mu            sync.Mutex
	throttleUntil time.Time
	dailyBlocked  bool
	dailyResetAt  time.Time
	dailySpent    int
	dailySpendDay time.Time

	bucket *quota.TokenBucket
	budget *quota.DailyBudget
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
	dailyReserve := cfg.DailyReserve
	if dailyReserve <= 0 {
		dailyReserve = defaultDailyReserve
	}
	minuteReserve := cfg.MinuteReserve
	if minuteReserve <= 0 {
		minuteReserve = defaultMinuteReserve
	}
	dailyLimit := cfg.DailyLimit
	if dailyLimit <= 0 {
		dailyLimit = defaultDailyLimit
	}
	minuteLimit := cfg.MinuteLimit
	if minuteLimit <= 0 {
		minuteLimit = defaultMinuteLimit
	}
	initialDailySpent := cfg.InitialDailySpent
	if initialDailySpent < 0 {
		initialDailySpent = 0
	}
	client := &Client{
		baseURL:       strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:        cfg.APIKey,
		dailyLimit:    dailyLimit,
		dailyReserve:  dailyReserve,
		minuteLimit:   minuteLimit,
		minuteReserve: minuteReserve,
		httpClient:    httpClient,
		now:           now,
		dailySpent:    initialDailySpent,
		dailySpendDay: utcDay(now()),
	}
	// Two independent quota controls: per-minute token bucket + daily budget.
	client.bucket = quota.NewTokenBucket(minuteLimit, now)
	client.budget = quota.NewDailyBudget(dailyLimit, dailyReserve, dailyLimit-initialDailySpent, now)
	return client
}

// DailyBudgetSnapshot exposes the current daily budget state so the quota
// planner can forecast from real remaining quota.
func (c *Client) DailyBudgetSnapshot() quota.DailySnapshot {
	if c.budget == nil {
		return quota.DailySnapshot{Limit: c.dailyLimit, Remaining: c.dailyLimit, Reserve: c.dailyReserve}
	}
	return c.budget.Snapshot()
}

// Observe re-seeds the daily budget from the provider's own accounting.
//
// Exported so the hourly quota sync can correct the live governor, not only
// the row it seeds from at startup. Without this, a governor that has talked
// itself into believing the quota is spent stays wrong until the next restart
// or until its own probing rescues it; with it, a wrong belief has a known
// upper bound of one hour even if everything else fails.
func (c *Client) Observe(limit, remaining int) {
	if c.budget == nil {
		return
	}
	c.budget.Observe(limit, remaining)
}

// ThrottleUntil returns the current minute-throttle deadline (zero when not
// throttled). The worker persists it to provider_rate_limits so throttle state
// survives restarts and is visible to operators.
func (c *Client) ThrottleUntil() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.throttleUntil.IsZero() {
		return time.Time{}
	}
	if !c.now().Before(c.throttleUntil) {
		c.throttleUntil = time.Time{}
		return time.Time{}
	}
	return c.throttleUntil
}

func (c *Client) Get(ctx context.Context, endpoint string, query url.Values) (provider.Response, error) {
	endpoint = strings.Trim(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return provider.Response{}, errors.New("provider endpoint is required")
	}
	if c.baseURL == "" {
		return provider.Response{}, errors.New("provider base URL is required")
	}
	if err := c.waitForAvailability(ctx); err != nil {
		return provider.Response{Provider: ProviderName, Endpoint: endpoint}, err
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
		req.Header.Set("x-apisports-key", c.apiKey)
	}

	// Counted here rather than on the response, because the provider counts
	// it here too. A request that times out, or whose body we fail to read,
	// has still been served and still debits the package; only a request we
	// never sent is free. Counting on success would make the ceiling leak by
	// exactly the number of requests that went wrong, which is highest on the
	// days it matters most.
	if c.budget != nil {
		c.budget.RecordIssued(1)
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
		RateLimit:     parseRateLimitHeaders(httpResp.Header),
	}
	c.updateAvailability(httpResp.StatusCode, httpResp.Header, resp.RateLimit)
	if readErr != nil {
		return resp, readErr
	}
	providerErr := providerErrorFromResponseBody(body)
	if providerErr == nil {
		providerErr = providerErrorFromHTTPStatus(httpResp.StatusCode)
	}
	if providerErr != nil {
		now := c.now()
		if retryAt := parseRetryAfter(httpResp.Header.Get("Retry-After"), now); !retryAt.IsZero() {
			providerErr.retryAfter = retryAt.Sub(now)
		}
		c.deferProviderCapacity(providerErr, now)
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		if providerErr != nil {
			return resp, fmt.Errorf("%w: status %d: %w", ErrProviderStatus, httpResp.StatusCode, providerErr)
		}
		return resp, fmt.Errorf("%w: %d", ErrProviderStatus, httpResp.StatusCode)
	}
	if providerErr != nil {
		return resp, providerErr
	}
	return resp, nil
}

func (c *Client) deferProviderCapacity(providerErr *ProviderError, now time.Time) {
	if providerErr == nil || !providerErr.Deferrable() {
		return
	}
	delay := providerErr.RetryAfter()
	if delay <= 0 {
		// No Retry-After. A full minute here is what strangled throughput.
		//
		// API-Football answers an over-rate request with HTTP 200 and a
		// rate-limit error in the body, carrying no headers. Two or three of
		// those a minute were arming a global sixty-second gate, and during
		// each gate every other request preflight-blocked: measured in
		// production at roughly 4,300 blocked against 10 sent per minute,
		// while x-ratelimit-remaining on the requests that did get through
		// read 414 to 449 out of 450. The account was spending 18,000 of
		// 75,000 requests a day and the rest expired unused.
		//
		// One rejected request is not evidence that the minute is spent. When
		// the last authoritative header said there is headroom, back off
		// briefly and let the next request try; reserve the full minute for
		// the case where the provider actually said to wait, or where the
		// observed allowance is genuinely gone.
		delay = shortRateLimitBackoff
		if c.bucket != nil && c.bucket.Remaining() <= c.minuteReserve {
			delay = time.Minute
		}
		providerErr.retryAfter = delay
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	until := now.Add(delay)
	if until.After(c.throttleUntil) {
		c.throttleUntil = until
	}
}

func (c *Client) waitForAvailability(ctx context.Context) error {
	// Daily budget governor: never let discretionary spend break the reserve.
	if c.budget != nil && !c.budget.CanSpend(1) {
		// Recheck soon: the effective reserve intentionally decays toward the
		// provider-day boundary, so waiting until midnight would strand quota.
		return &dailyQuotaReservedError{retryAfter: time.Minute}
	}

	// Minute throttle carried from the previous response (429 / Retry-After /
	// minute-exhausted header). Non-blocking: the caller releases the job and
	// the processor retries after retryAfter.
	now := c.now()
	c.mu.Lock()
	until := c.throttleUntil
	if !until.IsZero() && !now.Before(until) {
		c.throttleUntil = time.Time{}
		until = time.Time{}
	}
	c.mu.Unlock()
	if !until.IsZero() {
		return &rateLimitDeferredError{retryAfter: until.Sub(now)}
	}

	// Precise per-minute token-bucket gate: if the minute budget is exhausted
	// (no token available), defer with an estimate instead of blocking.
	if c.bucket != nil && !c.bucket.TryTake() {
		return &rateLimitDeferredError{retryAfter: c.bucket.WaitEstimate()}
	}
	return nil
}

func (c *Client) updateAvailability(status int, headers http.Header, rateLimit provider.RateLimitHeaders) {
	now := c.now()
	c.mu.Lock()
	day := utcDay(now)
	if !c.dailySpendDay.Equal(day) {
		c.dailySpendDay = day
		c.dailySpent = 0
	}
	if status > 0 {
		c.dailySpent++
	}

	if headers.Get("x-ratelimit-requests-remaining") != "" {
		if rateLimit.DailyRemaining <= c.dailyReserve {
			c.dailyBlocked = true
			c.dailyResetAt = now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
		} else {
			c.dailyBlocked = false
			c.dailyResetAt = time.Time{}
		}
	}
	if c.dailyLimit > 0 && c.dailySpent >= c.dailyLimit-c.dailyReserve {
		c.dailyBlocked = true
		c.dailyResetAt = day.Add(24 * time.Hour)
	}

	retryAfter := parseRetryAfter(headers.Get("Retry-After"), now)
	if !retryAfter.IsZero() && retryAfter.After(c.throttleUntil) {
		c.throttleUntil = retryAfter
	}
	hasMinuteRemaining := headers.Get("x-ratelimit-minute-remaining") != "" || headers.Get("X-RateLimit-Remaining") != ""
	minuteExhausted := hasMinuteRemaining && rateLimit.MinuteRemaining <= c.minuteReserve
	if (status == http.StatusTooManyRequests || minuteExhausted) && retryAfter.IsZero() {
		until := now.Add(time.Minute)
		if until.After(c.throttleUntil) {
			c.throttleUntil = until
		}
	}
	c.mu.Unlock()

	// Re-seed the two independent governors from authoritative provider headers.
	// Only reseed when the corresponding header is actually present; a missing
	// header must not be interpreted as "zero remaining".
	if c.bucket != nil && (headers.Get("x-ratelimit-minute-remaining") != "" || headers.Get("X-RateLimit-Remaining") != "") {
		c.bucket.Observe(rateLimit.MinuteLimit, rateLimit.MinuteRemaining)
	}
	if c.budget != nil {
		if headers.Get("x-ratelimit-requests-remaining") != "" {
			// The header already reflects this request; treat it as authoritative.
			c.budget.Observe(rateLimit.DailyLimit, rateLimit.DailyRemaining)
		} else {
			c.budget.Spend(1)
		}
	}
}

func utcDay(now time.Time) time.Time {
	return now.UTC().Truncate(24 * time.Hour)
}

func providerErrorFromResponseBody(body []byte) *ProviderError {
	var envelope struct {
		Errors json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil
	}
	return parseProviderError(envelope.Errors)
}

func providerErrorFromHTTPStatus(status int) *ProviderError {
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		return &ProviderError{Category: "parameters", Message: fmt.Sprintf("provider returned status %d", status)}
	case http.StatusUnauthorized, http.StatusForbidden:
		return &ProviderError{Category: "auth", Message: fmt.Sprintf("provider returned status %d", status)}
	case http.StatusTooManyRequests:
		return &ProviderError{Category: "rate", Message: "provider rate limit reached"}
	default:
		return nil
	}
}

func parseRetryAfter(raw string, now time.Time) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if parsed, err := http.ParseTime(raw); err == nil && parsed.After(now) {
		return parsed
	}
	return time.Time{}
}

func parseRateLimitHeaders(headers http.Header) provider.RateLimitHeaders {
	raw := map[string]string{}
	for _, key := range []string{
		"x-ratelimit-requests-limit",
		"x-ratelimit-requests-remaining",
		"x-ratelimit-minute-limit",
		"x-ratelimit-minute-remaining",
		"X-RateLimit-Limit",
		"X-RateLimit-Remaining",
		"Retry-After",
	} {
		if value := headers.Get(key); value != "" {
			raw[strings.ToLower(key)] = value
		}
	}
	return provider.RateLimitHeaders{
		DailyLimit:      parseHeaderInt(headers.Get("x-ratelimit-requests-limit")),
		DailyRemaining:  parseHeaderInt(headers.Get("x-ratelimit-requests-remaining")),
		MinuteLimit:     firstNonZero(parseHeaderInt(headers.Get("x-ratelimit-minute-limit")), parseHeaderInt(headers.Get("X-RateLimit-Limit"))),
		MinuteRemaining: firstNonZero(parseHeaderInt(headers.Get("x-ratelimit-minute-remaining")), parseHeaderInt(headers.Get("X-RateLimit-Remaining"))),
		Raw:             raw,
	}
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func parseHeaderInt(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return value
}
