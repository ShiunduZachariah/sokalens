package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// apiBaseURL is the production Bot API origin. The bot token is part of the
	// request path and therefore part of this URL; nothing in this package may
	// log a built URL (spec §8: the token is a production credential).
	apiBaseURL = "https://api.telegram.org"

	// maxAPIResponseBytes bounds every response body read. A hostile or broken
	// upstream must not be able to exhaust memory through a single reply.
	maxAPIResponseBytes = 1 << 20 // 1 MiB

	// minUpdatesLimit and maxUpdatesLimit are the Bot API's own bounds on
	// getUpdates.limit. Callers are clamped rather than rejected: an
	// out-of-range limit is a caller bug that must not stall the poller.
	minUpdatesLimit = 1
	maxUpdatesLimit = 100

	// maxPollTimeout bounds the long-poll duration a caller may request, so the
	// HTTP deadline below stays bounded too.
	maxPollTimeout = 60 * time.Second

	// httpTimeoutMargin is added to the long-poll timeout to derive the HTTP
	// deadline. Without the margin the transport deadline would expire at the
	// same moment the server is writing its empty result and every long poll
	// would fail.
	httpTimeoutMargin = 15 * time.Second

	// requestTimeout bounds the short, non-polling calls.
	requestTimeout = 30 * time.Second

	// maxMessageRunes is the Bot API's sendMessage text limit. The format layer
	// chunks well below this; the check here keeps a caller bug from becoming an
	// opaque 400 from Telegram.
	maxMessageRunes = 4096

	// tokenRedaction replaces the bot token anywhere it would otherwise reach
	// an error string.
	tokenRedaction = "<redacted>"
)

var (
	// ErrEmptyToken is a configuration error: the bridge must fail closed at
	// boot rather than long-poll an anonymous URL.
	ErrEmptyToken = errors.New("telegram: bot token is empty")
	// ErrEmptyMessage guards sendMessage against an empty body.
	ErrEmptyMessage = errors.New("telegram: message text is empty")
	// ErrMessageTooLong reports a body above the Bot API's 4096-character
	// limit; the caller is responsible for chunking (spec §11.2).
	ErrMessageTooLong = errors.New("telegram: message text exceeds the Bot API limit")
	// ErrResponseTooLarge reports a response body above maxAPIResponseBytes.
	ErrResponseTooLarge = errors.New("telegram: response body exceeds the read limit")
)

// HTTPClient is the concrete Client against api.telegram.org. Every field is
// set at construction and never mutated, so the value is safe for concurrent
// use by the poller and the send path simultaneously.
type HTTPClient struct {
	token      string
	baseURL    string
	httpClient *http.Client
}

var _ Client = (*HTTPClient)(nil)

// ClientOption configures NewClient. Both options exist so tests can point the
// client at an httptest server.
type ClientOption func(*clientOptions)

type clientOptions struct {
	baseURL    string
	httpClient *http.Client
}

// WithBaseURL overrides the api.telegram.org origin. The value must be an
// absolute http or https URL; a trailing slash is ignored.
func WithBaseURL(raw string) ClientOption {
	return func(o *clientOptions) { o.baseURL = raw }
}

// WithHTTPClient injects the transport. The client must not set a Timeout
// shorter than a saturated long poll plus httpTimeoutMargin; NewClient rejects
// one that does, because such a client makes every poll fail.
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(o *clientOptions) { o.httpClient = hc }
}

// NewClient validates the token and transport and returns a ready client. It
// performs no I/O: token validity is established by GetMe at boot (spec §7.3).
func NewClient(token string, opts ...ClientOption) (*HTTPClient, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrEmptyToken
	}
	options := clientOptions{baseURL: apiBaseURL}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}

	parsed, err := url.Parse(strings.TrimRight(options.baseURL, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("telegram: invalid base URL %q", options.baseURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("telegram: unsupported base URL scheme %q", parsed.Scheme)
	}

	httpClient := options.httpClient
	if httpClient == nil {
		// Deliberately no Timeout: a single global deadline cannot serve both a
		// 60s long poll and a 30s command call. Every request instead carries
		// its own context deadline, set in do.
		httpClient = &http.Client{}
	}
	if minimum := maxPollTimeout + httpTimeoutMargin; httpClient.Timeout > 0 && httpClient.Timeout < minimum {
		return nil, fmt.Errorf("telegram: http client timeout %s is below the %s a long poll requires", httpClient.Timeout, minimum)
	}

	return &HTTPClient{token: token, baseURL: parsed.String(), httpClient: httpClient}, nil
}

// ---------------------------------------------------------------------------
// Bot API methods.
// ---------------------------------------------------------------------------

type getUpdatesRequest struct {
	Offset         int64    `json:"offset"`
	Limit          int      `json:"limit"`
	Timeout        int      `json:"timeout"`
	AllowedUpdates []string `json:"allowed_updates"`
}

// GetUpdates long-polls. allowed_updates is pinned to ["message"] so no other
// update kind can ever reach the router.
func (c *HTTPClient) GetUpdates(ctx context.Context, offset int64, limit int, timeout time.Duration) ([]Update, error) {
	switch {
	case limit < minUpdatesLimit:
		limit = minUpdatesLimit
	case limit > maxUpdatesLimit:
		limit = maxUpdatesLimit
	}
	if timeout < 0 {
		timeout = 0
	}
	if timeout > maxPollTimeout {
		timeout = maxPollTimeout
	}

	payload := getUpdatesRequest{
		Offset:         offset,
		Limit:          limit,
		Timeout:        int(timeout / time.Second),
		AllowedUpdates: []string{"message"},
	}
	var updates []Update
	if err := c.do(ctx, "getUpdates", payload, timeout+httpTimeoutMargin, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

type sendMessageRequest struct {
	ChatID                int64  `json:"chat_id"`
	Text                  string `json:"text"`
	ParseMode             string `json:"parse_mode"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
}

// SendMessage delivers one already-chunked, already-escaped HTML part.
func (c *HTTPClient) SendMessage(ctx context.Context, chatID int64, html string) error {
	if html == "" {
		return ErrEmptyMessage
	}
	if utf8.RuneCountInString(html) > maxMessageRunes {
		return ErrMessageTooLong
	}
	payload := sendMessageRequest{
		ChatID:                chatID,
		Text:                  html,
		ParseMode:             "HTML",
		DisableWebPagePreview: true,
	}
	return c.do(ctx, "sendMessage", payload, requestTimeout, nil)
}

type deleteWebhookRequest struct {
	// DropPendingUpdates stays false: the queued updates are the ones the
	// poller is about to consume, and discarding them would lose commands.
	DropPendingUpdates bool `json:"drop_pending_updates"`
}

// DeleteWebhook runs unconditionally at boot, strictly before the first poll.
func (c *HTTPClient) DeleteWebhook(ctx context.Context) error {
	return c.do(ctx, "deleteWebhook", deleteWebhookRequest{DropPendingUpdates: false}, requestTimeout, nil)
}

// GetMe validates the token at boot.
func (c *HTTPClient) GetMe(ctx context.Context) (User, error) {
	var me User
	if err := c.do(ctx, "getMe", struct{}{}, requestTimeout, &me); err != nil {
		return User{}, err
	}
	return me, nil
}

// ---------------------------------------------------------------------------
// Transport.
// ---------------------------------------------------------------------------

// apiEnvelope is the wrapper Telegram puts around every response, success or
// failure.
type apiEnvelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  *struct {
		RetryAfter int64 `json:"retry_after"`
	} `json:"parameters"`
}

// do performs one Bot API call. deadline bounds the whole round trip; result,
// when non-nil, receives the decoded "result" member.
func (c *HTTPClient) do(ctx context.Context, method string, payload any, deadline time.Duration, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram: encode %s request: %w", method, err)
	}

	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: build %s request: %w", method, c.redact(err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: %s request failed: %w", method, c.redact(err))
	}
	defer func() { _ = resp.Body.Close() }()

	// Read one byte past the limit so an oversized body is detected instead of
	// being silently truncated into a decode error.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err != nil {
		return fmt.Errorf("telegram: read %s response: %w", method, c.redact(err))
	}
	if len(raw) > maxAPIResponseBytes {
		return fmt.Errorf("%w: %s returned more than %d bytes", ErrResponseTooLarge, method, maxAPIResponseBytes)
	}

	var env apiEnvelope
	decodeErr := json.Unmarshal(raw, &env)
	ok2xx := resp.StatusCode >= 200 && resp.StatusCode <= 299

	if decodeErr != nil {
		if !ok2xx {
			// An error status with an undecodable body is still an API error;
			// the body is not echoed, it may be an untrusted proxy page.
			return &APIError{Code: resp.StatusCode, Description: fmt.Sprintf("telegram: %s failed with status %d", method, resp.StatusCode)}
		}
		return fmt.Errorf("telegram: decode %s response: %w", method, decodeErr)
	}

	if !ok2xx || !env.OK {
		return newAPIError(method, resp.StatusCode, ok2xx, env)
	}

	if result == nil {
		return nil
	}
	if len(env.Result) == 0 {
		return fmt.Errorf("telegram: %s returned no result", method)
	}
	if err := json.Unmarshal(env.Result, result); err != nil {
		return fmt.Errorf("telegram: decode %s result: %w", method, err)
	}
	return nil
}

// newAPIError builds the typed error the poller switches on. The HTTP status is
// preferred, since that is what Telegram's own 409/401/429 semantics ride on;
// error_code is the fallback for the case Telegram returns ok:false under a
// 200, where the status carries no information.
func newAPIError(method string, status int, ok2xx bool, env apiEnvelope) *APIError {
	code := status
	if ok2xx && env.ErrorCode != 0 {
		code = env.ErrorCode
	}
	description := strings.TrimSpace(env.Description)
	if description == "" {
		description = fmt.Sprintf("telegram: %s failed with code %d", method, code)
	}
	apiErr := &APIError{Code: code, Description: description}
	if env.Parameters != nil && env.Parameters.RetryAfter > 0 {
		apiErr.RetryAfter = time.Duration(env.Parameters.RetryAfter) * time.Second
	}
	return apiErr
}

// endpoint builds the method URL. The token lives here and nowhere else; the
// returned string must never be logged or embedded in an error.
func (c *HTTPClient) endpoint(method string) string {
	return c.baseURL + "/bot" + c.token + "/" + method
}

// redactedError carries a token-free message while still unwrapping to the
// underlying cause, so callers can match context.DeadlineExceeded and net
// errors with errors.Is.
type redactedError struct {
	msg   string
	cause error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.cause }

// redact strips the bot token from an error. net/http returns *url.Error,
// whose message embeds the full request URL and therefore the token; the URL
// wrapper is dropped and any residual occurrence is replaced.
func (c *HTTPClient) redact(err error) error {
	if err == nil {
		return nil
	}
	cause := err
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		cause = urlErr.Err
	}
	msg := strings.ReplaceAll(cause.Error(), c.token, tokenRedaction)
	return &redactedError{msg: msg, cause: cause}
}
