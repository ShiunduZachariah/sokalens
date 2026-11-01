// Package sysmetrics is a minimal Prometheus instant-query client and the
// host-resource projection the Telegram /resources command renders.
//
// It is written against net/http and encoding/json on purpose. go.mod requires
// only github.com/jackc/pgx/v5, and pulling the official Prometheus client
// library (which drags in prometheus/common, prometheus/procfs and a large
// model package) to issue five GET requests would add a substantial transitive
// surface to an ops daemon that holds production credentials.
//
// This package is outbound only. It registers no route and listens on nothing:
// it calls GET /api/v1/query on the Prometheus that already runs in the compose
// network and projects the answer into a typed struct.
//
// Everything is bounded — query length, request timeout, response body size,
// and the number of samples decoded — because the reply to a malformed or
// hostile upstream must be an error, not memory exhaustion in the poller.
package sysmetrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is where Prometheus answers inside the compose network.
	// Verified reachable from the ops-controller container.
	DefaultBaseURL = "http://prometheus:9090"

	// DefaultTimeout bounds one query. /resources is answered from a phone
	// while something is on fire; a slow Prometheus must degrade to
	// "unavailable" quickly rather than hold the poller's handling path open.
	DefaultTimeout = 5 * time.Second

	// queryPath is the only endpoint this package calls.
	queryPath = "/api/v1/query"

	// userAgent identifies this client in Prometheus' access log.
	userAgent = "ops-controller"

	// maxResponseBytes bounds every response body read.
	maxResponseBytes = 1 << 20 // 1 MiB

	// maxSamples bounds the vector this client will decode. A host with more
	// than this many filesystems is not something a phone-sized reply can
	// usefully render anyway.
	maxSamples = 256

	// maxQueryChars bounds an outbound PromQL expression. Every query in this
	// package is a constant built here, so exceeding it is a programming
	// error rather than an input condition.
	maxQueryChars = 1024

	// maxMessageChars bounds an upstream-supplied error string before it is
	// placed in an error value.
	maxMessageChars = 200
)

var (
	// ErrEmptyQuery reports a blank PromQL expression.
	ErrEmptyQuery = errors.New("sysmetrics: query is empty")
	// ErrQueryTooLong reports a PromQL expression above maxQueryChars.
	ErrQueryTooLong = errors.New("sysmetrics: query is too long")

	// ErrUnreachable means the request never produced an HTTP response:
	// DNS failure, refused connection, timeout, cancelled context. This is
	// "Prometheus is not there", distinct from every answer it can give.
	ErrUnreachable = errors.New("sysmetrics: prometheus is unreachable")

	// ErrNoData means Prometheus was reached, answered success, and returned
	// an empty vector. The metric is not being scraped, or the selector
	// matched nothing. It is deliberately distinct from ErrUnreachable and
	// from an APIError: "no data" is a different operational fact from
	// "Prometheus is down" and from "Prometheus rejected the query".
	ErrNoData = errors.New("sysmetrics: query returned no samples")

	// ErrResponseTooLarge reports a body above maxResponseBytes.
	ErrResponseTooLarge = errors.New("sysmetrics: response body exceeds the read limit")

	// ErrMalformedResponse reports a body that is not a Prometheus
	// instant-vector envelope, or a sample value that is not a finite number.
	ErrMalformedResponse = errors.New("sysmetrics: response is not a Prometheus instant-vector envelope")
)

// APIError is returned when Prometheus answered but the answer was not a
// successful query result: a non-2xx status, or a 2xx carrying
// status != "success". Both are "reached Prometheus, it refused", which the
// caller must be able to tell apart from ErrUnreachable and from ErrNoData.
type APIError struct {
	// Query is the PromQL that failed. It is always one of this package's own
	// constants, never operator input.
	Query string
	// StatusCode is the HTTP status. Always set.
	StatusCode int
	// Status is the envelope's "status" field, empty when the body did not
	// parse as an envelope.
	Status string
	// ErrorType is the envelope's "errorType" ("bad_data", "timeout", ...).
	ErrorType string
	// Message is the envelope's "error" field, bounded.
	Message string
}

func (e *APIError) Error() string {
	var b strings.Builder
	b.WriteString("sysmetrics: prometheus query failed (http ")
	b.WriteString(strconv.Itoa(e.StatusCode))
	if e.Status != "" && e.Status != "success" {
		b.WriteString(", status ")
		b.WriteString(e.Status)
	}
	b.WriteString(")")
	if e.ErrorType != "" {
		b.WriteString(": ")
		b.WriteString(e.ErrorType)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

// IsHTTP reports whether the failure was the HTTP status rather than the
// query envelope.
func (e *APIError) IsHTTP() bool { return e.StatusCode < 200 || e.StatusCode > 299 }

// Sample is one entry of an instant vector.
type Sample struct {
	// Labels are the sample's metric labels. Never nil.
	Labels map[string]string
	// Value is the parsed sample value. Always finite: non-finite samples are
	// rejected as malformed rather than propagated into a percentage.
	Value float64
}

// Label reads one label, returning "" when absent.
func (s Sample) Label(name string) string { return s.Labels[name] }

// Client issues instant queries. Every field is set at construction and never
// mutated, so the value is safe for concurrent use.
type Client struct {
	baseURL    string
	timeout    time.Duration
	httpClient *http.Client
}

// Option configures New. Both exist so tests can point the client at an
// httptest server without a network.
type Option func(*options)

type options struct {
	timeout    time.Duration
	httpClient *http.Client
}

// WithHTTPClient injects the transport.
func WithHTTPClient(hc *http.Client) Option {
	return func(o *options) { o.httpClient = hc }
}

// WithTimeout overrides DefaultTimeout. A non-positive value is ignored: this
// client is never unbounded.
func WithTimeout(d time.Duration) Option {
	return func(o *options) { o.timeout = d }
}

// New validates its inputs and returns a ready client. It performs no I/O: an
// unreachable Prometheus is discovered on the first real query and degrades to
// "unavailable", rather than blocking boot on a monitoring dependency.
//
// An empty baseURL means DefaultBaseURL. The URL is a parameter rather than a
// read from internal/config so this package has no configuration dependency.
func New(baseURL string, opts ...Option) (*Client, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	settings := options{timeout: DefaultTimeout}
	for _, opt := range opts {
		if opt != nil {
			opt(&settings)
		}
	}
	if settings.timeout <= 0 {
		settings.timeout = DefaultTimeout
	}

	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("sysmetrics: invalid base URL %q", baseURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("sysmetrics: unsupported base URL scheme %q", parsed.Scheme)
	}

	httpClient := settings.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: settings.timeout}
	}

	return &Client{
		baseURL:    parsed.String(),
		timeout:    settings.timeout,
		httpClient: httpClient,
	}, nil
}

// BaseURL reports the configured origin, for log lines. It carries no
// credential: this client authenticates with nothing.
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.baseURL
}

// Timeout reports the per-query bound.
func (c *Client) Timeout() time.Duration {
	if c == nil {
		return DefaultTimeout
	}
	return c.timeout
}

// envelope is the standard Prometheus HTTP API response. Only the fields this
// package reads are modelled.
type envelope struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			// value is [ <unix_time float>, "<sample value string>" ]. The
			// second element is a *string* on the wire, which is why this is
			// json.RawMessage rather than float64: decoding it as a number
			// fails, and decoding the pair as []any loses precision and the
			// ability to reject a non-numeric value cleanly.
			Value []json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// Query runs one instant query and returns its vector.
//
// The four failure modes are distinguishable by the caller:
//
//	errors.Is(err, ErrUnreachable)  — no HTTP response at all
//	errors.As(err, &apiErr)         — Prometheus answered and refused
//	errors.Is(err, ErrNoData)       — Prometheus answered, vector was empty
//	errors.Is(err, ErrMalformed…)   — Prometheus answered something unparseable
func (c *Client) Query(ctx context.Context, promql string) ([]Sample, error) {
	if c == nil || c.httpClient == nil {
		return nil, fmt.Errorf("%w: client is not configured", ErrUnreachable)
	}
	promql = strings.TrimSpace(promql)
	if promql == "" {
		return nil, ErrEmptyQuery
	}
	if len(promql) > maxQueryChars {
		return nil, ErrQueryTooLong
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// url.Values.Encode percent-encodes the expression, so the regex matchers
	// in the disk queries survive transport intact.
	params := url.Values{}
	params.Set("query", promql)
	params.Set("timeout", strconv.Itoa(int(c.timeout/time.Second))+"s")
	target := c.baseURL + queryPath + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("sysmetrics: build query request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Refused, DNS failure, TLS failure, deadline: no answer was produced,
		// which is materially different from an answer this client disliked.
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read one byte past the limit so an oversized body is detected rather
	// than silently truncated into a confusing decode error.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %v", ErrUnreachable, err)
	}
	if len(raw) > maxResponseBytes {
		return nil, fmt.Errorf("%w: %d bytes or more", ErrResponseTooLarge, len(raw))
	}

	var env envelope
	decodeErr := json.Unmarshal(raw, &env)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Prometheus reports a bad query as 400 with a populated envelope, so
		// the decoded fields are used when they are there and skipped when
		// the body was, say, an HTML proxy error page.
		return nil, &APIError{
			Query:      promql,
			StatusCode: resp.StatusCode,
			Status:     env.Status,
			ErrorType:  env.ErrorType,
			Message:    truncate(env.Error, maxMessageChars),
		}
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedResponse, decodeErr)
	}
	if env.Status != "success" {
		return nil, &APIError{
			Query:      promql,
			StatusCode: resp.StatusCode,
			Status:     env.Status,
			ErrorType:  env.ErrorType,
			Message:    truncate(env.Error, maxMessageChars),
		}
	}
	// Only instant vectors are requested, so anything else means the response
	// does not describe what was asked for. Guessing at a matrix or a scalar
	// here would produce a number that looks right and is not.
	if env.Data.ResultType != "vector" {
		return nil, fmt.Errorf("%w: resultType %q", ErrMalformedResponse, env.Data.ResultType)
	}

	results := env.Data.Result
	if len(results) > maxSamples {
		results = results[:maxSamples]
	}
	samples := make([]Sample, 0, len(results))
	for _, r := range results {
		if len(r.Value) < 2 {
			return nil, fmt.Errorf("%w: sample has no value pair", ErrMalformedResponse)
		}
		var text string
		if err := json.Unmarshal(r.Value[1], &text); err != nil {
			return nil, fmt.Errorf("%w: sample value is not a string", ErrMalformedResponse)
		}
		v, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: sample value %q is not a number", ErrMalformedResponse, truncate(text, 32))
		}
		// NaN and ±Inf parse cleanly and then render as "NaN%" in a reply.
		// A gauge that has gone non-finite is missing data, not a reading.
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		labels := r.Metric
		if labels == nil {
			labels = map[string]string{}
		}
		samples = append(samples, Sample{Labels: labels, Value: v})
	}
	if len(samples) == 0 {
		return nil, ErrNoData
	}
	return samples, nil
}

// QueryScalar runs a query expected to yield exactly one series and returns
// its value. More than one series means the aggregation in the query was
// wrong, which is a bug here rather than an upstream condition; the first
// sample is used and the rest ignored, because a partially-correct memory
// figure still beats an "unavailable" that hides a real reading.
func (c *Client) QueryScalar(ctx context.Context, promql string) (float64, error) {
	samples, err := c.Query(ctx, promql)
	if err != nil {
		return 0, err
	}
	return samples[0].Value, nil
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
