// Package github is a minimal GitHub REST client covering exactly the three
// calls the Telegram /issue command needs: create an issue, search open issues,
// and comment on an existing one.
//
// It is written against net/http and encoding/json on purpose. Recon assumption
// A6 recorded that this service has no GitHub dependency, and go.mod requires
// only github.com/jackc/pgx/v5; pulling in go-github to POST three endpoints
// would add a large transitive surface to an ops daemon that holds production
// credentials. Nothing here is a general-purpose client — it is the smallest
// thing that files an issue correctly.
//
// This package is outbound only. It registers no route and listens on nothing.
//
// The token is a production credential (spec §18). It travels in a header, is
// never placed in a URL, is never logged, and scrub() removes it from every
// error string this package constructs, so a mirrored Authorization header in a
// hostile response body cannot leak it into a Telegram message.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the public REST origin. Tests override it.
	DefaultBaseURL = "https://api.github.com"

	// APIVersion is sent as X-GitHub-Api-Version on every request. Pinning it
	// explicitly is what keeps a future default-version bump on GitHub's side
	// from silently changing the shape of a response this client parses.
	APIVersion = "2022-11-28"

	// acceptHeader selects the versioned JSON media type.
	acceptHeader = "application/vnd.github+json"

	// userAgent is required by the REST API; requests without one are refused.
	userAgent = "ops-controller"

	// maxResponseBytes bounds every response body read. A hostile or broken
	// upstream must not be able to exhaust memory through a single reply.
	maxResponseBytes = 1 << 20 // 1 MiB

	// DefaultTimeout bounds one round trip when the caller's context has no
	// earlier deadline.
	DefaultTimeout = 15 * time.Second

	// maxSearchResults bounds a search page. The dedup check only ever needs
	// the first match; asking for a full page of 100 buys nothing.
	maxSearchResults = 20

	// maxRetryAfter caps a server-supplied backoff. GitHub has been observed to
	// return very large rate-limit resets; a caller that slept on one blindly
	// would look like a hang.
	maxRetryAfter = time.Hour

	// tokenRedaction replaces the token anywhere it would otherwise reach an
	// error string.
	tokenRedaction = "<redacted>"
)

var (
	// ErrEmptyToken is a configuration error: an unauthenticated client cannot
	// file anything, and failing at construction is better than at 3am.
	ErrEmptyToken = errors.New("github: token is empty")
	// ErrEmptyRepository reports a missing owner or repository name.
	ErrEmptyRepository = errors.New("github: owner and repository are required")
	// ErrEmptyTitle guards CreateIssue against an untitled issue.
	ErrEmptyTitle = errors.New("github: issue title is empty")
	// ErrEmptyBody guards CommentOnIssue against an empty comment.
	ErrEmptyBody = errors.New("github: body is empty")
	// ErrEmptyQuery guards SearchOpenIssues against an unbounded search.
	ErrEmptyQuery = errors.New("github: search query is empty")
	// ErrInvalidIssueNumber reports a non-positive issue number.
	ErrInvalidIssueNumber = errors.New("github: issue number must be positive")
	// ErrResponseTooLarge reports a response body above maxResponseBytes.
	ErrResponseTooLarge = errors.New("github: response body exceeds the read limit")
)

// APIError is returned for every non-2xx response. The status code is carried
// separately from the message because the caller's decision — retry, give up,
// or tell the operator to fix a credential — turns on the code alone.
type APIError struct {
	// Op names the call that failed ("create issue", "search issues").
	Op string
	// StatusCode is the HTTP status. It is always set.
	StatusCode int
	// Message is GitHub's own "message" field, scrubbed. It may be empty.
	Message string
	// RetryAfter carries the Retry-After header, or the time until the
	// rate-limit reset on a 429 or a secondary limit. Zero when neither
	// applies.
	RetryAfter time.Duration
	// RateLimited marks a primary (429) or secondary (403) rate limit.
	RateLimited bool
}

// Error always names the status code. GitHub can return an error with an empty
// message body, and a blank error string would lose the one field that tells
// the caller whether the condition is worth retrying.
func (e *APIError) Error() string {
	op := e.Op
	if op == "" {
		op = "request"
	}
	if e.Message == "" {
		return "github: " + op + " failed with status " + strconv.Itoa(e.StatusCode)
	}
	return "github: " + op + " failed with status " + strconv.Itoa(e.StatusCode) + ": " + e.Message
}

// IsUnauthorized reports a 401: the token is missing, revoked or malformed.
// Never retried — a retry loop on a dead credential hides the outage.
func (e *APIError) IsUnauthorized() bool { return e.StatusCode == http.StatusUnauthorized }

// IsForbidden reports a 403 that is not a rate limit: the token is valid but
// lacks the scope, or the repository is archived. Also permanent.
func (e *APIError) IsForbidden() bool {
	return e.StatusCode == http.StatusForbidden && !e.RateLimited
}

// IsNotFound reports a 404. On the GitHub API this is ambiguous by design: a
// private repository the token cannot see returns 404 rather than 403, so this
// usually means "wrong repo or insufficient scope", not "deleted".
func (e *APIError) IsNotFound() bool { return e.StatusCode == http.StatusNotFound }

// IsValidation reports a 422: the request was understood and rejected — an
// unknown label, a title over the limit. Retrying it unchanged never helps.
func (e *APIError) IsValidation() bool { return e.StatusCode == http.StatusUnprocessableEntity }

// IsRateLimited reports a primary or secondary rate limit. Honour RetryAfter.
func (e *APIError) IsRateLimited() bool { return e.RateLimited }

// IsServer reports a 5xx: GitHub is broken, the request probably was not.
func (e *APIError) IsServer() bool { return e.StatusCode >= 500 }

// IsRetryable reports whether repeating the identical request could succeed.
// Everything in the 401/403/404/422 family is excluded: those are conditions a
// human has to change something to clear.
func (e *APIError) IsRetryable() bool { return e.IsServer() || e.RateLimited }

// Client is the concrete REST client. Every field is set at construction and
// never mutated, so the value is safe for concurrent use.
type Client struct {
	owner      string
	repo       string
	token      string
	baseURL    string
	httpClient *http.Client
}

// Option configures New. Both options exist so tests can point the client at an
// httptest server without a network.
type Option func(*options)

type options struct {
	baseURL    string
	httpClient *http.Client
}

// WithBaseURL overrides the api.github.com origin. The value must be an
// absolute http or https URL; a trailing slash is ignored.
func WithBaseURL(raw string) Option {
	return func(o *options) { o.baseURL = raw }
}

// WithHTTPClient injects the transport.
func WithHTTPClient(hc *http.Client) Option {
	return func(o *options) { o.httpClient = hc }
}

// New validates its inputs and returns a ready client. It performs no I/O: a
// bad token is discovered on the first real call, and the caller degrades
// rather than blocking boot on GitHub's availability.
//
// Owner, repository, token and base URL are all parameters rather than reads
// from internal/config, so this package has no configuration dependency and a
// test can construct one in a line.
func New(owner, repo, token string, opts ...Option) (*Client, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if owner == "" || repo == "" {
		return nil, ErrEmptyRepository
	}
	if strings.TrimSpace(token) == "" {
		return nil, ErrEmptyToken
	}

	settings := options{baseURL: DefaultBaseURL}
	for _, opt := range opts {
		if opt != nil {
			opt(&settings)
		}
	}

	parsed, err := url.Parse(strings.TrimRight(settings.baseURL, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("github: invalid base URL %q", settings.baseURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("github: unsupported base URL scheme %q", parsed.Scheme)
	}

	httpClient := settings.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}

	return &Client{
		owner:      owner,
		repo:       repo,
		token:      strings.TrimSpace(token),
		baseURL:    parsed.String(),
		httpClient: httpClient,
	}, nil
}

// Repository renders "owner/repo", for search qualifiers and log lines. It
// carries no credential.
func (c *Client) Repository() string {
	if c == nil {
		return ""
	}
	return c.owner + "/" + c.repo
}

// ---------------------------------------------------------------------------
// Wire types. Only the fields this service actually reads are modelled.
// ---------------------------------------------------------------------------

// IssueRequest is the create-issue payload.
type IssueRequest struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels,omitempty"`
}

// Issue is the subset of a GitHub issue this service uses.
type Issue struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
}

// Comment is the subset of an issue comment this service uses.
type Comment struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
}

type commentRequest struct {
	Body string `json:"body"`
}

type searchResult struct {
	TotalCount int     `json:"total_count"`
	Items      []Issue `json:"items"`
}

// ---------------------------------------------------------------------------
// Calls.
// ---------------------------------------------------------------------------

// CreateIssue files a new issue. The caller is responsible for redaction: this
// package sends the strings it is given.
func (c *Client) CreateIssue(ctx context.Context, req IssueRequest) (Issue, error) {
	if c == nil {
		return Issue{}, ErrEmptyRepository
	}
	if strings.TrimSpace(req.Title) == "" {
		return Issue{}, ErrEmptyTitle
	}
	var issue Issue
	path := "/repos/" + url.PathEscape(c.owner) + "/" + url.PathEscape(c.repo) + "/issues"
	if err := c.do(ctx, "create issue", http.MethodPost, path, req, &issue); err != nil {
		return Issue{}, err
	}
	return issue, nil
}

// SearchOpenIssues finds open issues in this repository matching query.
//
// The repo, is:issue and is:open qualifiers are appended here rather than left
// to the caller: a dedup search that accidentally ran repository-wide, or that
// matched a closed issue, would comment on the wrong thing.
func (c *Client) SearchOpenIssues(ctx context.Context, query string) ([]Issue, error) {
	if c == nil {
		return nil, ErrEmptyRepository
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrEmptyQuery
	}

	full := query + " repo:" + c.owner + "/" + c.repo + " is:issue is:open"
	values := url.Values{}
	values.Set("q", full)
	values.Set("per_page", strconv.Itoa(maxSearchResults))

	var result searchResult
	if err := c.do(ctx, "search issues", http.MethodGet, "/search/issues?"+values.Encode(), nil, &result); err != nil {
		return nil, err
	}
	if len(result.Items) > maxSearchResults {
		result.Items = result.Items[:maxSearchResults]
	}
	return result.Items, nil
}

// CommentOnIssue appends a comment to an existing issue.
func (c *Client) CommentOnIssue(ctx context.Context, number int, body string) (Comment, error) {
	if c == nil {
		return Comment{}, ErrEmptyRepository
	}
	if number <= 0 {
		return Comment{}, ErrInvalidIssueNumber
	}
	if strings.TrimSpace(body) == "" {
		return Comment{}, ErrEmptyBody
	}
	var comment Comment
	path := "/repos/" + url.PathEscape(c.owner) + "/" + url.PathEscape(c.repo) +
		"/issues/" + strconv.Itoa(number) + "/comments"
	if err := c.do(ctx, "comment on issue", http.MethodPost, path, commentRequest{Body: body}, &comment); err != nil {
		return Comment{}, err
	}
	return comment, nil
}

// ---------------------------------------------------------------------------
// Transport.
// ---------------------------------------------------------------------------

// errorEnvelope is GitHub's error shape. The per-field "errors" array is read
// only to give a 422 a usable message; field values are not echoed.
type errorEnvelope struct {
	Message string `json:"message"`
	Errors  []struct {
		Resource string `json:"resource"`
		Field    string `json:"field"`
		Code     string `json:"code"`
	} `json:"errors"`
}

// do performs one call. payload is JSON-encoded when non-nil; result, when
// non-nil, receives the decoded response.
func (c *Client) do(ctx context.Context, op, method, path string, payload any, result any) error {
	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("github: encode %s request: %w", op, err)
		}
		reader = bytes.NewReader(encoded)
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("github: build %s request: %w", op, c.scrubErr(err))
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("X-GitHub-Api-Version", APIVersion)
	req.Header.Set("User-Agent", userAgent)
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// A transport error can quote the request URL; the URL never carries
		// the token, but scrubbing costs nothing and the invariant is worth
		// more than the micro-optimisation.
		return fmt.Errorf("github: %s request failed: %w", op, c.scrubErr(err))
	}
	defer func() { _ = resp.Body.Close() }()

	// Read one byte past the limit so an oversized body is detected rather than
	// silently truncated into a confusing decode error.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("github: read %s response: %w", op, c.scrubErr(err))
	}
	if len(raw) > maxResponseBytes {
		return fmt.Errorf("%w: %s returned more than %d bytes", ErrResponseTooLarge, op, maxResponseBytes)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.apiError(op, resp, raw)
	}
	if result == nil {
		return nil
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("github: %s returned an empty body", op)
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return fmt.Errorf("github: decode %s response: %w", op, c.scrubErr(err))
	}
	return nil
}

// apiError builds the typed error. The response body is parsed for GitHub's
// own message, but is never echoed verbatim: a non-2xx from a proxy in front of
// the API can be an arbitrary HTML page.
func (c *Client) apiError(op string, resp *http.Response, raw []byte) *APIError {
	apiErr := &APIError{Op: op, StatusCode: resp.StatusCode}

	var env errorEnvelope
	if err := json.Unmarshal(raw, &env); err == nil {
		apiErr.Message = c.scrub(strings.TrimSpace(env.Message))
		if apiErr.Message != "" && len(env.Errors) > 0 {
			first := env.Errors[0]
			if first.Field != "" || first.Code != "" {
				apiErr.Message += " (" + c.scrub(strings.TrimSpace(first.Resource+"."+first.Field+" "+first.Code)) + ")"
			}
		}
	}

	apiErr.RateLimited = isRateLimited(resp, apiErr.Message)
	apiErr.RetryAfter = retryAfter(resp, apiErr.RateLimited)
	return apiErr
}

// isRateLimited distinguishes the two shapes GitHub uses. A primary limit is a
// 429; a secondary limit is a 403 whose remaining-quota header is zero or whose
// message says so. Treating every 403 as a rate limit would make a missing
// scope look retryable forever.
func isRateLimited(resp *http.Response, message string) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if resp.StatusCode != http.StatusForbidden {
		return false
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return true
	}
	lowered := strings.ToLower(message)
	return strings.Contains(lowered, "rate limit") || strings.Contains(lowered, "abuse detection")
}

// retryAfter reads the server's own backoff instruction. Retry-After wins; the
// rate-limit reset epoch is the fallback for the secondary-limit responses that
// omit it.
func retryAfter(resp *http.Response, rateLimited bool) time.Duration {
	if header := strings.TrimSpace(resp.Header.Get("Retry-After")); header != "" {
		if seconds, err := strconv.ParseInt(header, 10, 64); err == nil && seconds > 0 {
			return capDuration(time.Duration(seconds) * time.Second)
		}
		if when, err := http.ParseTime(header); err == nil {
			if d := time.Until(when); d > 0 {
				return capDuration(d)
			}
		}
	}
	if !rateLimited {
		return 0
	}
	if reset := strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")); reset != "" {
		if epoch, err := strconv.ParseInt(reset, 10, 64); err == nil {
			if d := time.Until(time.Unix(epoch, 0)); d > 0 {
				return capDuration(d)
			}
		}
	}
	return 0
}

func capDuration(d time.Duration) time.Duration {
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

// scrub removes the token from a string. It is the last line of defence for
// spec §18: nothing this package returns may carry the credential, whatever an
// upstream chose to echo back.
func (c *Client) scrub(s string) string {
	if c == nil || len(c.token) < 8 || s == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, tokenRedaction)
}

// scrubErr wraps scrub for an error value, preserving nothing but the message.
// The original error is deliberately not wrapped: errors.Unwrap on it would
// hand the caller back the unscrubbed text.
func (c *Client) scrubErr(err error) error {
	if err == nil {
		return nil
	}
	scrubbed := c.scrub(err.Error())
	if scrubbed == err.Error() {
		return err
	}
	return errors.New(scrubbed)
}
