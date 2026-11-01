package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testToken is the fake credential every test plants. Nothing this package
// returns may contain it.
var testToken = "ghp" + "_" + "thisIsAFakeTokenForTestsOnly0123456789"

func newTestClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New("acme", "platform", testToken, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client, server
}

func TestNewValidatesInputs(t *testing.T) {
	cases := []struct {
		name             string
		owner, repo, tok string
		opts             []Option
		wantErr          error
		wantErrSubstring string
	}{
		{name: "no owner", repo: "platform", tok: testToken, wantErr: ErrEmptyRepository},
		{name: "no repo", owner: "acme", tok: testToken, wantErr: ErrEmptyRepository},
		{name: "no token", owner: "acme", repo: "platform", wantErr: ErrEmptyToken},
		{name: "blank token", owner: "acme", repo: "platform", tok: "   ", wantErr: ErrEmptyToken},
		{
			name: "bad scheme", owner: "acme", repo: "platform", tok: testToken,
			opts: []Option{WithBaseURL("ftp://example.invalid")}, wantErrSubstring: "scheme",
		},
		{
			name: "not a URL", owner: "acme", repo: "platform", tok: testToken,
			opts: []Option{WithBaseURL("not a url")}, wantErrSubstring: "invalid base URL",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.owner, tc.repo, tc.tok, tc.opts...)
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
			if tc.wantErrSubstring != "" && !strings.Contains(err.Error(), tc.wantErrSubstring) {
				t.Fatalf("got %q, want it to mention %q", err, tc.wantErrSubstring)
			}
			assertNoToken(t, err.Error())
		})
	}
}

func TestCreateIssueSendsHeadersAndPayload(t *testing.T) {
	var gotAuth, gotAccept, gotVersion, gotAgent, gotMethod, gotPath string
	var gotBody IssueRequest

	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotVersion = r.Header.Get("X-GitHub-Api-Version")
		gotAgent = r.Header.Get("User-Agent")
		gotMethod, gotPath = r.Method, r.URL.Path
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":412,"html_url":"https://github.com/acme/platform/issues/412","state":"open"}`))
	}))

	issue, err := client.CreateIssue(context.Background(), IssueRequest{
		Title:  "Incident #143",
		Body:   "body",
		Labels: []string{"ops", "incident"},
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if issue.Number != 412 || issue.HTMLURL == "" {
		t.Fatalf("decoded issue = %+v", issue)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/repos/acme/platform/issues" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotAuth != "Bearer "+testToken {
		t.Fatalf("authorization header = %q", gotAuth)
	}
	if gotAccept != acceptHeader {
		t.Fatalf("accept = %q, want %q", gotAccept, acceptHeader)
	}
	if gotVersion != APIVersion {
		t.Fatalf("api version = %q, want %q", gotVersion, APIVersion)
	}
	if gotAgent != userAgent {
		t.Fatalf("user agent = %q", gotAgent)
	}
	if gotBody.Title != "Incident #143" || len(gotBody.Labels) != 2 {
		t.Fatalf("payload = %+v", gotBody)
	}
}

func TestCreateIssueRejectsEmptyTitle(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("no request should have been sent")
	}))
	if _, err := client.CreateIssue(context.Background(), IssueRequest{Body: "b"}); !errors.Is(err, ErrEmptyTitle) {
		t.Fatalf("got %v, want ErrEmptyTitle", err)
	}
}

func TestSearchOpenIssuesScopesTheQuery(t *testing.T) {
	var gotQuery, gotPerPage string
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotPerPage = r.URL.Query().Get("per_page")
		_, _ = w.Write([]byte(`{"total_count":1,"items":[{"number":77,"state":"open","html_url":"https://github.com/acme/platform/issues/77"}]}`))
	}))

	items, err := client.SearchOpenIssues(context.Background(), `"ops-fingerprint: abc123" in:body`)
	if err != nil {
		t.Fatalf("SearchOpenIssues: %v", err)
	}
	if len(items) != 1 || items[0].Number != 77 {
		t.Fatalf("items = %+v", items)
	}
	for _, qualifier := range []string{"repo:acme/platform", "is:issue", "is:open", "ops-fingerprint: abc123"} {
		if !strings.Contains(gotQuery, qualifier) {
			t.Fatalf("query %q is missing %q", gotQuery, qualifier)
		}
	}
	if gotPerPage == "" {
		t.Fatal("per_page was not bounded")
	}
}

func TestSearchOpenIssuesRejectsEmptyQuery(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("no request should have been sent")
	}))
	if _, err := client.SearchOpenIssues(context.Background(), "  "); !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("got %v, want ErrEmptyQuery", err)
	}
}

func TestCommentOnIssue(t *testing.T) {
	var gotPath string
	var gotBody commentRequest
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":9001,"html_url":"https://github.com/acme/platform/issues/77#issuecomment-9001"}`))
	}))

	comment, err := client.CommentOnIssue(context.Background(), 77, "recurred")
	if err != nil {
		t.Fatalf("CommentOnIssue: %v", err)
	}
	if comment.ID != 9001 || comment.HTMLURL == "" {
		t.Fatalf("comment = %+v", comment)
	}
	if gotPath != "/repos/acme/platform/issues/77/comments" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotBody.Body != "recurred" {
		t.Fatalf("body = %q", gotBody.Body)
	}
}

func TestCommentOnIssueValidatesArguments(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("no request should have been sent")
	}))
	if _, err := client.CommentOnIssue(context.Background(), 0, "x"); !errors.Is(err, ErrInvalidIssueNumber) {
		t.Fatalf("got %v, want ErrInvalidIssueNumber", err)
	}
	if _, err := client.CommentOnIssue(context.Background(), 5, " "); !errors.Is(err, ErrEmptyBody) {
		t.Fatalf("got %v, want ErrEmptyBody", err)
	}
}

// TestStatusClassification is the table the caller's retry decision rests on.
// 401/403/404/422 are permanent; 5xx and rate limits are not.
func TestStatusClassification(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		headers     map[string]string
		body        string
		wantAuth    bool
		wantForbid  bool
		wantMissing bool
		wantInvalid bool
		wantServer  bool
		wantLimited bool
		wantRetry   bool
	}{
		{name: "401", status: 401, body: `{"message":"Bad credentials"}`, wantAuth: true},
		{name: "403 scope", status: 403, body: `{"message":"Resource not accessible by personal access token"}`, wantForbid: true},
		{name: "404", status: 404, body: `{"message":"Not Found"}`, wantMissing: true},
		{name: "422", status: 422, body: `{"message":"Validation Failed","errors":[{"resource":"Issue","field":"labels","code":"invalid"}]}`, wantInvalid: true},
		{name: "500", status: 500, body: `{"message":"Server Error"}`, wantServer: true, wantRetry: true},
		{name: "502 html body", status: 502, body: `<html>bad gateway</html>`, wantServer: true, wantRetry: true},
		{
			name: "429", status: 429, headers: map[string]string{"Retry-After": "42"},
			body: `{"message":"API rate limit exceeded"}`, wantLimited: true, wantRetry: true,
		},
		{
			name: "403 secondary limit", status: 403,
			headers:     map[string]string{"X-RateLimit-Remaining": "0", "Retry-After": "7"},
			body:        `{"message":"You have exceeded a secondary rate limit"}`,
			wantLimited: true, wantRetry: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			_, err := client.CreateIssue(context.Background(), IssueRequest{Title: "t", Body: "b"})
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("got %v, want *APIError", err)
			}
			if apiErr.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", apiErr.StatusCode, tc.status)
			}
			if apiErr.IsUnauthorized() != tc.wantAuth ||
				apiErr.IsForbidden() != tc.wantForbid ||
				apiErr.IsNotFound() != tc.wantMissing ||
				apiErr.IsValidation() != tc.wantInvalid ||
				apiErr.IsServer() != tc.wantServer ||
				apiErr.IsRateLimited() != tc.wantLimited ||
				apiErr.IsRetryable() != tc.wantRetry {
				t.Fatalf("classification mismatch for %s: %+v", tc.name, apiErr)
			}
			if !strings.Contains(apiErr.Error(), "create issue") {
				t.Fatalf("error does not name the operation: %s", apiErr.Error())
			}
			assertNoToken(t, apiErr.Error())
		})
	}
}

func TestRetryAfterHonoursHeaderThenResetEpoch(t *testing.T) {
	t.Run("retry-after seconds", func(t *testing.T) {
		client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "31")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"rate limited"}`))
		}))
		var apiErr *APIError
		_, err := client.CreateIssue(context.Background(), IssueRequest{Title: "t"})
		if !errors.As(err, &apiErr) || apiErr.RetryAfter != 31*time.Second {
			t.Fatalf("RetryAfter = %v, want 31s (err %v)", apiErr.RetryAfter, err)
		}
	})

	t.Run("secondary limit reset epoch", func(t *testing.T) {
		reset := time.Now().Add(90 * time.Second).Unix()
		client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit"}`))
		}))
		var apiErr *APIError
		_, err := client.CreateIssue(context.Background(), IssueRequest{Title: "t"})
		if !errors.As(err, &apiErr) {
			t.Fatalf("got %v, want *APIError", err)
		}
		if apiErr.RetryAfter <= 0 || apiErr.RetryAfter > 91*time.Second {
			t.Fatalf("RetryAfter = %v, want roughly 90s", apiErr.RetryAfter)
		}
	})

	t.Run("no backoff on a plain 500", func(t *testing.T) {
		client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		var apiErr *APIError
		_, err := client.CreateIssue(context.Background(), IssueRequest{Title: "t"})
		if !errors.As(err, &apiErr) || apiErr.RetryAfter != 0 {
			t.Fatalf("RetryAfter = %v, want 0 (err %v)", apiErr.RetryAfter, err)
		}
	})
}

// TestResponseBodyIsBounded proves io.LimitReader is actually in the path: a
// server that streams forever must not be able to exhaust memory.
func TestResponseBodyIsBounded(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		chunk := strings.Repeat("a", 64*1024)
		// Well past maxResponseBytes; the client must stop reading.
		for i := 0; i < 40; i++ {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
	}))
	_, err := client.CreateIssue(context.Background(), IssueRequest{Title: "t"})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("got %v, want ErrResponseTooLarge", err)
	}
	assertNoToken(t, err.Error())
}

// TestTokenNeverReachesAnError is the §18 invariant. Every failure mode is
// exercised, including a hostile server that mirrors the Authorization header
// straight back into its error message.
func TestTokenNeverReachesAnError(t *testing.T) {
	t.Run("mirrored in the error message", func(t *testing.T) {
		client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			payload, _ := json.Marshal(errorEnvelope{Message: "rejected credential " + r.Header.Get("Authorization")})
			_, _ = w.Write(payload)
		}))
		_, err := client.CreateIssue(context.Background(), IssueRequest{Title: "t"})
		if err == nil {
			t.Fatal("expected an error")
		}
		assertNoToken(t, err.Error())
		if !strings.Contains(err.Error(), tokenRedaction) {
			t.Fatalf("expected the redaction marker, got %q", err)
		}
	})

	t.Run("transport failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		client, err := New("acme", "platform", testToken, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
		if err != nil {
			t.Fatal(err)
		}
		server.Close() // the connection now fails
		_, err = client.CreateIssue(context.Background(), IssueRequest{Title: "t"})
		if err == nil {
			t.Fatal("expected a transport error")
		}
		assertNoToken(t, err.Error())
	})

	t.Run("undecodable success body", func(t *testing.T) {
		client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{not json`))
		}))
		_, err := client.CreateIssue(context.Background(), IssueRequest{Title: "t"})
		if err == nil {
			t.Fatal("expected a decode error")
		}
		assertNoToken(t, err.Error())
	})
}

func TestRepository(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if got := client.Repository(); got != "acme/platform" {
		t.Fatalf("Repository() = %q", got)
	}
	var nilClient *Client
	if got := nilClient.Repository(); got != "" {
		t.Fatalf("nil Repository() = %q, want empty", got)
	}
}

func assertNoToken(t *testing.T, s string) {
	t.Helper()
	if strings.Contains(s, testToken) {
		t.Fatalf("the token leaked into %q", s)
	}
}
