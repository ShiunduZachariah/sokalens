package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// testToken is deliberately distinctive so the leak assertions cannot pass by
// accident.
var testToken = fakeBotToken()

type capturedRequest struct {
	method      string
	path        string
	contentType string
	body        map[string]any
}

// newTestClient starts an httptest server running handler and returns a client
// pointed at it.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*HTTPClient, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := NewClient(testToken, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client, server
}

// captureHandler records the first request and replies with raw.
func captureHandler(t *testing.T, captured *capturedRequest, status int, raw string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		captured.method = r.Method
		captured.path = r.URL.Path
		captured.contentType = r.Header.Get("Content-Type")
		captured.body = map[string]any{}
		if err := json.Unmarshal(body, &captured.body); err != nil {
			t.Errorf("decode request body %q: %v", string(body), err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, raw)
	}
}

func TestGetUpdatesSendsPinnedAllowedUpdatesAndParameters(t *testing.T) {
	var got capturedRequest
	const body = `{"ok":true,"result":[{"update_id":42,"message":{"message_id":7,"date":1700000000,"text":"/status","from":{"id":99,"is_bot":false,"username":"owner"},"chat":{"id":-100,"type":"group"}}}]}`
	client, _ := newTestClient(t, captureHandler(t, &got, http.StatusOK, body))

	updates, err := client.GetUpdates(context.Background(), 41, 25, 30*time.Second)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}

	if got.method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.method)
	}
	if want := "/bot" + testToken + "/getUpdates"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	if !strings.HasPrefix(got.contentType, "application/json") {
		t.Errorf("content type = %q, want application/json", got.contentType)
	}
	if v, ok := got.body["offset"].(float64); !ok || int64(v) != 41 {
		t.Errorf("offset = %v, want 41", got.body["offset"])
	}
	if v, ok := got.body["limit"].(float64); !ok || int(v) != 25 {
		t.Errorf("limit = %v, want 25", got.body["limit"])
	}
	if v, ok := got.body["timeout"].(float64); !ok || int(v) != 30 {
		t.Errorf("timeout = %v, want 30 (seconds)", got.body["timeout"])
	}
	allowed, ok := got.body["allowed_updates"].([]any)
	if !ok || len(allowed) != 1 || allowed[0] != "message" {
		t.Fatalf("allowed_updates = %v, want [\"message\"]", got.body["allowed_updates"])
	}

	if len(updates) != 1 {
		t.Fatalf("got %d updates, want 1", len(updates))
	}
	if updates[0].UpdateID != 42 {
		t.Errorf("update id = %d, want 42", updates[0].UpdateID)
	}
	if updates[0].Message == nil || updates[0].Message.Text != "/status" {
		t.Fatalf("message not decoded: %+v", updates[0].Message)
	}
	if updates[0].Message.From == nil || updates[0].Message.From.ID != 99 {
		t.Errorf("from not decoded: %+v", updates[0].Message.From)
	}
	if updates[0].Message.Chat.ID != -100 {
		t.Errorf("chat id = %d, want -100", updates[0].Message.Chat.ID)
	}
}

func TestGetUpdatesClampsLimitAndTimeout(t *testing.T) {
	tests := []struct {
		name        string
		limit       int
		timeout     time.Duration
		wantLimit   int
		wantTimeout int
	}{
		{name: "below range", limit: 0, timeout: -time.Second, wantLimit: minUpdatesLimit, wantTimeout: 0},
		{name: "above range", limit: 5000, timeout: 10 * time.Minute, wantLimit: maxUpdatesLimit, wantTimeout: int(maxPollTimeout / time.Second)},
		{name: "in range", limit: 100, timeout: 1500 * time.Millisecond, wantLimit: 100, wantTimeout: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got capturedRequest
			client, _ := newTestClient(t, captureHandler(t, &got, http.StatusOK, `{"ok":true,"result":[]}`))

			if _, err := client.GetUpdates(context.Background(), 0, tc.limit, tc.timeout); err != nil {
				t.Fatalf("GetUpdates: %v", err)
			}
			if v, _ := got.body["limit"].(float64); int(v) != tc.wantLimit {
				t.Errorf("limit = %v, want %d", got.body["limit"], tc.wantLimit)
			}
			if v, _ := got.body["timeout"].(float64); int(v) != tc.wantTimeout {
				t.Errorf("timeout = %v, want %d", got.body["timeout"], tc.wantTimeout)
			}
		})
	}
}

// A long poll must outlive the timeout it asked Telegram to hold the
// connection for; the HTTP deadline carries httpTimeoutMargin on top.
func TestGetUpdatesSurvivesAFullyHeldLongPoll(t *testing.T) {
	const poll = 300 * time.Millisecond
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(poll):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
	})

	updates, err := client.GetUpdates(context.Background(), 0, 100, poll)
	if err != nil {
		t.Fatalf("GetUpdates held for the full poll window: %v", err)
	}
	if len(updates) != 0 {
		t.Fatalf("got %d updates, want 0", len(updates))
	}
}

func TestGetUpdatesConflictIsFatalAPIError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":409,"description":"Conflict: terminated by other getUpdates request"}`)
	})

	_, err := client.GetUpdates(context.Background(), 0, 100, 0)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T), want *APIError", err, err)
	}
	if !apiErr.IsConflict() {
		t.Errorf("IsConflict() = false for code %d", apiErr.Code)
	}
	if apiErr.IsUnauthorized() {
		t.Errorf("IsUnauthorized() = true for a 409")
	}
	if !strings.Contains(apiErr.Description, "terminated by other getUpdates request") {
		t.Errorf("description = %q, want Telegram's own text", apiErr.Description)
	}
}

func TestUnauthorizedIsFatalAPIError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	})

	_, err := client.GetMe(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T), want *APIError", err, err)
	}
	if !apiErr.IsUnauthorized() {
		t.Errorf("IsUnauthorized() = false for code %d", apiErr.Code)
	}
	if apiErr.RetryAfter != 0 {
		t.Errorf("RetryAfter = %s, want 0", apiErr.RetryAfter)
	}
}

func TestTooManyRequestsCarriesRetryAfter(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 30","parameters":{"retry_after":30}}`)
	})

	err := client.SendMessage(context.Background(), -100, "<b>hi</b>")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T), want *APIError", err, err)
	}
	if apiErr.Code != http.StatusTooManyRequests {
		t.Errorf("code = %d, want 429", apiErr.Code)
	}
	if apiErr.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %s, want 30s", apiErr.RetryAfter)
	}
}

// Telegram can answer HTTP 200 with ok:false. A 2xx status must not be read as
// success.
func TestOKFalseUnder200IsStillAnError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
	})

	err := client.SendMessage(context.Background(), -100, "<b>hi</b>")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T), want *APIError", err, err)
	}
	if apiErr.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want the body's error_code 400", apiErr.Code)
	}
	if apiErr.Description != "Bad Request: chat not found" {
		t.Errorf("description = %q", apiErr.Description)
	}
}

func TestOKFalseWithoutErrorCodeFallsBackToStatus(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":false}`)
	})

	err := client.DeleteWebhook(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T), want *APIError", err, err)
	}
	if apiErr.Code != http.StatusOK {
		t.Errorf("code = %d, want the HTTP status fallback 200", apiErr.Code)
	}
	if apiErr.Error() == "" {
		t.Error("Error() is empty; a description must always be synthesised")
	}
}

func TestOversizedResponseBodyIsRejected(t *testing.T) {
	const oversize = maxAPIResponseBytes + (1 << 16)
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":"`)
		chunk := strings.Repeat("A", 1<<16)
		for written := 0; written < oversize; written += len(chunk) {
			if _, err := io.WriteString(w, chunk); err != nil {
				return // the client hung up, which is the behaviour under test
			}
		}
		_, _ = io.WriteString(w, `"}`)
	})

	_, err := client.GetUpdates(context.Background(), 0, 100, 0)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Error("an oversized body must not be reported as an API error")
	}
}

func TestResponseAtTheLimitIsStillDecoded(t *testing.T) {
	// A body that exactly fills the limit must succeed: the guard is strictly
	// "more than", so a legitimate 100-update batch is never dropped.
	padding := strings.Repeat("B", maxAPIResponseBytes-len(`{"ok":true,"result":[],"description":""}`))
	body := `{"ok":true,"result":[],"description":"` + padding + `"}`
	if len(body) != maxAPIResponseBytes {
		t.Fatalf("test body is %d bytes, want exactly %d", len(body), maxAPIResponseBytes)
	}
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})

	if _, err := client.GetUpdates(context.Background(), 0, 100, 0); err != nil {
		t.Fatalf("GetUpdates on a body exactly at the limit: %v", err)
	}
}

func TestSendMessagePayload(t *testing.T) {
	var got capturedRequest
	client, _ := newTestClient(t, captureHandler(t, &got, http.StatusOK, `{"ok":true,"result":{"message_id":5}}`))

	if err := client.SendMessage(context.Background(), -1001234567890, "<b>ok</b> &amp; done"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if want := "/bot" + testToken + "/sendMessage"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	if v, _ := got.body["chat_id"].(float64); int64(v) != -1001234567890 {
		t.Errorf("chat_id = %v", got.body["chat_id"])
	}
	if got.body["text"] != "<b>ok</b> &amp; done" {
		t.Errorf("text = %v", got.body["text"])
	}
	if got.body["parse_mode"] != "HTML" {
		t.Errorf("parse_mode = %v, want HTML", got.body["parse_mode"])
	}
	if got.body["disable_web_page_preview"] != true {
		t.Errorf("disable_web_page_preview = %v, want true", got.body["disable_web_page_preview"])
	}
}

func TestSendMessageRejectsUnsendableBodies(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should reach Telegram")
	})

	if err := client.SendMessage(context.Background(), 1, ""); !errors.Is(err, ErrEmptyMessage) {
		t.Errorf("empty body error = %v, want ErrEmptyMessage", err)
	}
	oversized := strings.Repeat("x", maxMessageRunes+1)
	if err := client.SendMessage(context.Background(), 1, oversized); !errors.Is(err, ErrMessageTooLong) {
		t.Errorf("oversized body error = %v, want ErrMessageTooLong", err)
	}
}

func TestDeleteWebhookDoesNotDropPendingUpdates(t *testing.T) {
	var got capturedRequest
	client, _ := newTestClient(t, captureHandler(t, &got, http.StatusOK, `{"ok":true,"result":true}`))

	if err := client.DeleteWebhook(context.Background()); err != nil {
		t.Fatalf("DeleteWebhook: %v", err)
	}
	if want := "/bot" + testToken + "/deleteWebhook"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	if got.body["drop_pending_updates"] != false {
		t.Errorf("drop_pending_updates = %v, want false", got.body["drop_pending_updates"])
	}
}

func TestGetMeDecodesTheBotUser(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if want := "/bot" + testToken + "/getMe"; r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"id":8123456789,"is_bot":true,"username":"sokalens_ops_bot","first_name":"SokaLens Ops"}}`)
	})

	me, err := client.GetMe(context.Background())
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if me.ID != 8123456789 || !me.IsBot || me.Username != "sokalens_ops_bot" {
		t.Fatalf("GetMe returned %+v", me)
	}
}

// The token is a production credential. No failure mode may put it in an error
// string, because errors reach logs and Telegram messages.
func TestTokenNeverAppearsInErrors(t *testing.T) {
	unreachable := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	unreachableURL := unreachable.URL
	unreachable.Close()

	deadClient, err := NewClient(testToken, WithBaseURL(unreachableURL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "transport failure carries a url.Error with the token in it",
			call: func() error {
				_, err := deadClient.GetUpdates(context.Background(), 0, 100, 0)
				return err
			},
		},
		{
			name: "cancelled context",
			call: func() error {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return deadClient.DeleteWebhook(ctx)
			},
		},
		{
			name: "api error",
			call: func() error {
				client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
				})
				_, err := client.GetMe(context.Background())
				return err
			},
		},
		{
			name: "unparseable error body",
			call: func() error {
				client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusBadGateway)
					_, _ = io.WriteString(w, "<html>bad gateway</html>")
				})
				_, err := client.GetUpdates(context.Background(), 0, 100, 0)
				return err
			},
		},
		{
			name: "unparseable success body",
			call: func() error {
				client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.WriteString(w, "not json")
				})
				_, err := client.GetUpdates(context.Background(), 0, 100, 0)
				return err
			},
		},
		{
			name: "result of the wrong shape",
			call: func() error {
				client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.WriteString(w, `{"ok":true,"result":{"not":"an array"}}`)
				})
				_, err := client.GetUpdates(context.Background(), 0, 100, 0)
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("want an error")
			}
			if strings.Contains(err.Error(), testToken) {
				t.Fatalf("error leaked the bot token: %q", err.Error())
			}
			// A partial leak is still a leak: the secret half of the token must
			// not survive either.
			secret := testToken[strings.Index(testToken, ":")+1:]
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaked the token secret: %q", err.Error())
			}
		})
	}
}

// The redaction must survive unwrapping, so callers can still match sentinels
// without recovering the URL.
func TestTransportErrorPreservesSentinelWithoutLeakingTheURL(t *testing.T) {
	// The handler must be released by the test rather than by the request
	// context: a client-side cancellation is not observed by the server until it
	// next reads the connection, so blocking on r.Context() would wedge
	// httptest.Server.Close.
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		slow.Close()
	})

	client, err := NewClient(testToken, WithBaseURL(slow.URL), WithHTTPClient(slow.Client()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err = client.DeleteWebhook(ctx)
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("errors.Is(err, context.DeadlineExceeded) = false for %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("timeout error leaked the token: %q", err.Error())
	}
	var urlErr interface{ Timeout() bool }
	if errors.As(err, &urlErr) {
		// *url.Error satisfies this too; make sure whatever we expose has no URL.
		if strings.Contains(err.Error(), slow.URL) {
			t.Errorf("error exposed the request URL: %q", err.Error())
		}
	}
}

func TestNewClientValidation(t *testing.T) {
	if _, err := NewClient(""); !errors.Is(err, ErrEmptyToken) {
		t.Errorf("empty token error = %v, want ErrEmptyToken", err)
	}
	if _, err := NewClient("   "); !errors.Is(err, ErrEmptyToken) {
		t.Errorf("blank token error = %v, want ErrEmptyToken", err)
	}
	if _, err := NewClient(testToken, WithBaseURL("not-a-url")); err == nil {
		t.Error("relative base URL accepted")
	}
	if _, err := NewClient(testToken, WithBaseURL("ftp://example.invalid")); err == nil {
		t.Error("non-http base URL accepted")
	}

	// An HTTP client whose global timeout is shorter than a saturated long poll
	// would make every poll fail; that must be a construction error, not a
	// runtime mystery.
	short := &http.Client{Timeout: 10 * time.Second}
	if _, err := NewClient(testToken, WithHTTPClient(short)); err == nil {
		t.Error("http client with a sub-long-poll timeout accepted")
	} else if strings.Contains(err.Error(), testToken) {
		t.Errorf("construction error leaked the token: %q", err.Error())
	}

	generous := &http.Client{Timeout: maxPollTimeout + httpTimeoutMargin}
	if _, err := NewClient(testToken, WithHTTPClient(generous)); err != nil {
		t.Errorf("http client with an adequate timeout rejected: %v", err)
	}

	client, err := NewClient(testToken, WithBaseURL("https://example.invalid/"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if want := "https://example.invalid/bot" + testToken + "/getMe"; client.endpoint("getMe") != want {
		t.Errorf("endpoint = %q, want %q", client.endpoint("getMe"), want)
	}
}

// The poller calls GetUpdates while the notification path calls SendMessage;
// the contract requires the implementation to be safe for concurrent use.
func TestClientIsSafeForConcurrentUse(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
		default:
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
		}
	})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if _, err := client.GetUpdates(context.Background(), int64(j), 100, 0); err != nil {
					t.Errorf("GetUpdates: %v", err)
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if err := client.SendMessage(context.Background(), -100, "<b>ping</b>"); err != nil {
					t.Errorf("SendMessage: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// fakeBotToken builds a Telegram-shaped bot token at run time.
//
// The literal must never appear in source: gitleaks matches this shape, not
// its meaning, so even an obviously fake value fails the repo's secret-scan
// gate. .gitleaks.toml records the same convention for the ACCA cursor
// fixture — construct the value rather than allowlist it.
func fakeBotToken() string {
	return "8123456789" + ":" + "AA" + strings.Repeat("z", 34)
}
