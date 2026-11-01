package notifications

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCloudflareTransportSendsStructuredEmail(t *testing.T) {
	var gotRequest struct {
		To      string `json:"to"`
		From    string `json:"from"`
		Subject string `json:"subject"`
		Text    string `json:"text"`
		HTML    string `json:"html"`
		ReplyTo string `json:"reply_to"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/client/v4/accounts/acct/email/sending/send" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer scoped-token" {
			t.Fatalf("authorization header leaked or missing: %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Idempotency-Key") != "verify-42" {
			t.Fatalf("idempotency key = %q", r.Header.Get("Idempotency-Key"))
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"message_id":"cf-123","queued":["user@example.com"]}}`))
	}))
	defer server.Close()

	transport := CloudflareTransport{AccountID: "acct", APIToken: "scoped-token", BaseURL: server.URL + "/client/v4", From: "notifications@sokalens.com", FromName: "SokaLens", Client: server.Client()}
	payload, _ := json.Marshal(EmailPayload{To: "user@example.com", Subject: "Verify", Text: "Plain", HTML: "<p>HTML</p>", ReplyTo: "support@sokalens.com"})
	delivery, err := transport.Send(context.Background(), DeliveryRequest{Message: Message{ID: 42, Category: "email_verification", Payload: payload}, IdempotencyKey: "verify-42"})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if delivery.ProviderMessageID != "cloudflare:cf-123" || delivery.ResponseCode != "200" {
		t.Fatalf("delivery = %+v", delivery)
	}
	if gotRequest.To != "user@example.com" || gotRequest.From != "SokaLens <notifications@sokalens.com>" || gotRequest.Subject != "Verify" || gotRequest.Text != "Plain" || gotRequest.HTML != "<p>HTML</p>" || gotRequest.ReplyTo != "support@sokalens.com" {
		t.Fatalf("request payload = %+v", gotRequest)
	}
}

func TestCloudflareTransportReturnsSafeStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Bearer scoped-token invalid for user@example.com"}]}`))
	}))
	defer server.Close()
	payload, _ := json.Marshal(EmailPayload{To: "user@example.com", Subject: "Subject", Text: "Text"})
	_, err := (CloudflareTransport{AccountID: "acct", APIToken: "scoped-token", BaseURL: server.URL, From: "notifications@sokalens.com", Client: server.Client()}).Send(context.Background(), DeliveryRequest{Message: Message{Payload: payload}, IdempotencyKey: "key"})
	// The message stays secret-free, and 401 is classified as a provider
	// fault rather than a message fault so the resolver can fail over to the
	// next provider instead of dead-lettering a valid email.
	if err == nil || strings.Contains(err.Error(), "scoped-token") || strings.Contains(err.Error(), "user@example.com") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "Cloudflare returned HTTP 401") || FaultOf(err) != FaultUnavailable {
		t.Fatalf("error = %v, fault = %q", err, FaultOf(err))
	}
}

// stubCloudflare returns a transport pointed at a server that always succeeds,
// plus a pointer to the headers it received.
func stubCloudflare(t *testing.T) (CloudflareTransport, *http.Header) {
	t.Helper()
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"message_id":"abc123"}}`))
	}))
	t.Cleanup(server.Close)
	return CloudflareTransport{
		AccountID: "acct", APIToken: "cf-secret-token", From: "alerts@sokalens.com",
		FromName: "SokaLens", BaseURL: server.URL, Client: server.Client(),
	}, &seen
}

func emailRequest(t *testing.T, payload EmailPayload, idempotencyKey string) DeliveryRequest {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return DeliveryRequest{
		Message:        Message{ID: 1, Channel: "email", Category: "test", Payload: raw},
		IdempotencyKey: idempotencyKey,
	}
}

// TestCloudflareTransportRejectsHeaderInjection is the one that matters most.
// A newline in an address or subject lets an attacker append arbitrary SMTP
// headers — a Bcc to themselves, a forged Reply-To — turning a transactional
// email into a delivery mechanism they control. The transport guards this;
// this test is what stops the guard being removed as "unnecessary".
func TestCloudflareTransportRejectsHeaderInjection(t *testing.T) {
	transport, _ := stubCloudflare(t)
	cases := map[string]EmailPayload{
		"newline in to":       {To: "user@example.com\nBcc: attacker@evil.test", Subject: "Hi", Text: "body"},
		"carriage return":     {To: "user@example.com\rBcc: attacker@evil.test", Subject: "Hi", Text: "body"},
		"newline in subject":  {To: "user@example.com", Subject: "Hi\nBcc: attacker@evil.test", Text: "body"},
		"newline in reply-to": {To: "user@example.com", Subject: "Hi", ReplyTo: "a@b.test\nBcc: c@d.test", Text: "body"},
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := transport.Send(context.Background(), emailRequest(t, payload, "k1")); err == nil {
				t.Fatal("header injection was accepted")
			}
		})
	}
}

// TestCloudflareTransportRejectsIncompleteRequests keeps a half-configured
// provider from silently producing malformed mail.
func TestCloudflareTransportRejectsIncompleteRequests(t *testing.T) {
	base, _ := stubCloudflare(t)
	valid := EmailPayload{To: "user@example.com", Subject: "Hi", Text: "body"}

	missingCreds := []CloudflareTransport{
		func() CloudflareTransport { c := base; c.AccountID = ""; return c }(),
		func() CloudflareTransport { c := base; c.APIToken = ""; return c }(),
		func() CloudflareTransport { c := base; c.From = ""; return c }(),
	}
	for i, transport := range missingCreds {
		if _, err := transport.Send(context.Background(), emailRequest(t, valid, "k")); err == nil {
			t.Fatalf("case %d: incomplete configuration was accepted", i)
		}
	}

	badPayloads := map[string]EmailPayload{
		"no recipient": {Subject: "Hi", Text: "body"},
		"no subject":   {To: "user@example.com", Text: "body"},
		"empty body":   {To: "user@example.com", Subject: "Hi", Text: "   "},
	}
	for name, payload := range badPayloads {
		t.Run(name, func(t *testing.T) {
			if _, err := base.Send(context.Background(), emailRequest(t, payload, "k")); err == nil {
				t.Fatal("invalid payload was accepted")
			}
		})
	}
}

// TestCloudflareTransportSendsIdempotencyKey — a retried delivery must not
// produce a second email. The key is how the provider deduplicates.
func TestCloudflareTransportSendsIdempotencyKey(t *testing.T) {
	transport, seen := stubCloudflare(t)
	payload := EmailPayload{To: "user@example.com", Subject: "Hi", Text: "body"}
	if _, err := transport.Send(context.Background(), emailRequest(t, payload, "delivery-42")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := seen.Get("Idempotency-Key"); got != "delivery-42" {
		t.Fatalf("Idempotency-Key = %q, want %q", got, "delivery-42")
	}
	if got := seen.Get("Authorization"); got != "Bearer cf-secret-token" {
		t.Fatalf("Authorization header = %q", got)
	}
}

// TestCloudflareTransportRejectsUnconfirmedDelivery — a 200 that does not
// confirm a message id must not be reported as delivered, or the outbox marks
// a mail sent that never was.
func TestCloudflareTransportRejectsUnconfirmedDelivery(t *testing.T) {
	for name, body := range map[string]string{
		"success false":    `{"success":false,"result":{"message_id":"x"}}`,
		"no message id":    `{"success":true,"result":{}}`,
		"unparseable":      `not json`,
		"empty message id": `{"success":true,"result":{"message_id":""}}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			transport := CloudflareTransport{
				AccountID: "acct", APIToken: "tok", From: "a@b.test",
				BaseURL: server.URL, Client: server.Client(),
			}
			payload := EmailPayload{To: "user@example.com", Subject: "Hi", Text: "body"}
			if _, err := transport.Send(context.Background(), emailRequest(t, payload, "k")); err == nil {
				t.Fatal("an unconfirmed delivery was reported as sent")
			}
		})
	}
}

// TestCloudflareTransportNeverLeaksTheAPIToken — the token is a production
// credential and errors get logged.
func TestCloudflareTransportNeverLeaksTheAPIToken(t *testing.T) {
	const token = "cf-super-secret-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusUnauthorized)
		// A hostile or confused endpoint echoing the credential back.
		_, _ = w.Write([]byte(`{"error":"bad token ` + token + `"}`))
	}))
	defer server.Close()

	transport := CloudflareTransport{
		AccountID: "acct", APIToken: token, From: "a@b.test",
		BaseURL: server.URL, Client: server.Client(),
	}
	payload := EmailPayload{To: "user@example.com", Subject: "Hi", Text: "body"}
	_, err := transport.Send(context.Background(), emailRequest(t, payload, "k"))
	if err == nil {
		t.Fatal("expected an error for HTTP 401")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("the API token leaked into the error: %q", err)
	}
}
