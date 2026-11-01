package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strings"
	"testing"
)

func bulkMessage(t *testing.T, payload EmailPayload) Message {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return Message{Channel: "email", Category: "digest", Payload: encoded, DedupeKey: "digest:1"}
}

func TestBulkEmailMustCarryAnUnsubscribeHeader(t *testing.T) {
	message := bulkMessage(t, EmailPayload{To: "fan@example.com", Subject: "Your week", Text: "..."})
	_, err := messageRequirements(message)
	if !IsPermanentFault(err) {
		t.Fatalf("err = %v, fault = %q", err, FaultOf(err))
	}
	if !strings.Contains(err.Error(), "List-Unsubscribe") {
		t.Fatalf("error does not name the missing header: %v", err)
	}
}

func TestTransactionalEmailMustNotOfferUnsubscribe(t *testing.T) {
	payload, _ := json.Marshal(EmailPayload{To: "fan@example.com", Subject: "Reset", Text: "...", ListUnsubscribe: "https://sokalens.com/unsubscribe?token=abc"})
	// Offering to switch off password-reset mail would let a user disable the
	// only channel that can recover their account.
	_, err := messageRequirements(Message{Channel: "email", Category: "password_reset", Payload: payload})
	if !IsPermanentFault(err) {
		t.Fatalf("err = %v, fault = %q", err, FaultOf(err))
	}
}

func TestTransactionalEmailNeedsNoUnsubscribeHeader(t *testing.T) {
	payload, _ := json.Marshal(EmailPayload{To: "fan@example.com", Subject: "Verify", Text: "..."})
	required, err := messageRequirements(Message{Channel: "email", Category: "email_verification", Payload: payload})
	if err != nil || required != 0 {
		t.Fatalf("required = %d, err = %v", required, err)
	}
}

// A category nobody remembered to classify is treated as bulk, so the gap
// shows up as a refused send rather than as non-compliant mail.
func TestAnUnknownCategoryIsTreatedAsBulk(t *testing.T) {
	if !RequiresListUnsubscribe("email", "a_category_added_next_month") {
		t.Fatal("an unclassified email category must default to bulk")
	}
	if RequiresListUnsubscribe("in_app", "digest") {
		t.Fatal("only the email channel carries unsubscribe headers")
	}
}

func TestUnsubscribeHeadersAdvertiseOneClickOnlyWithHTTPS(t *testing.T) {
	both, err := unsubscribeHeaders(EmailPayload{ListUnsubscribe: "https://sokalens.com/unsubscribe?token=abc", ListUnsubscribeMailto: "mailto:unsubscribe@sokalens.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if both["List-Unsubscribe"] != "<https://sokalens.com/unsubscribe?token=abc>, <mailto:unsubscribe@sokalens.com>" {
		t.Fatalf("List-Unsubscribe = %q", both["List-Unsubscribe"])
	}
	if both["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
		t.Fatalf("List-Unsubscribe-Post = %q", both["List-Unsubscribe-Post"])
	}

	// RFC 8058 one-click is a POST. A mailto-only list cannot honour one, and
	// advertising it fails a mailbox provider's automated check.
	mailtoOnly, err := unsubscribeHeaders(EmailPayload{ListUnsubscribeMailto: "mailto:unsubscribe@sokalens.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := mailtoOnly["List-Unsubscribe-Post"]; ok {
		t.Fatal("one-click must not be advertised without an HTTPS target")
	}
}

func TestUnsubscribeHeadersRejectInjectionAndPlainHTTP(t *testing.T) {
	for name, payload := range map[string]EmailPayload{
		"newline":     {ListUnsubscribe: "https://sokalens.com/u\r\nBcc: attacker@example.com"},
		"delimiter":   {ListUnsubscribe: "https://sokalens.com/u>, <https://evil.example.com"},
		"plain http":  {ListUnsubscribe: "http://sokalens.com/unsubscribe"},
		"relative":    {ListUnsubscribe: "/unsubscribe"},
		"bare mailto": {ListUnsubscribeMailto: "unsubscribe@sokalens.com"},
	} {
		if _, err := unsubscribeHeaders(payload); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestSMTPRendersUnsubscribeHeaders(t *testing.T) {
	var body string
	transport := SMTPTransport{
		Address: "mailhog:1025",
		From:    "notifications@sokalens.local",
		SendMail: func(_ string, _ smtp.Auth, _ string, _ []string, message []byte) error {
			body = string(message)
			return nil
		},
	}
	payload, _ := json.Marshal(EmailPayload{To: "fan@example.com", Subject: "Your week", Text: "...", ListUnsubscribe: "https://sokalens.com/unsubscribe?token=abc"})
	if _, err := transport.Send(context.Background(), DeliveryRequest{Message: Message{Payload: payload}, IdempotencyKey: "digest-1"}); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if !strings.Contains(body, "List-Unsubscribe: <https://sokalens.com/unsubscribe?token=abc>\r\n") {
		t.Fatalf("headers missing from message: %q", body)
	}
	if !strings.Contains(body, "List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n") {
		t.Fatalf("one-click header missing: %q", body)
	}
	// The headers must precede the MIME body, not land inside it.
	if strings.Index(body, "List-Unsubscribe:") > strings.Index(body, "MIME-Version:") {
		t.Fatalf("headers rendered after MIME-Version: %q", body)
	}
}

func TestResendSendsUnsubscribeHeaders(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&received)
		_, _ = w.Write([]byte(`{"id":"re-1"}`))
	}))
	defer server.Close()
	payload, _ := json.Marshal(EmailPayload{To: "fan@example.com", Subject: "Your week", Text: "...", ListUnsubscribe: "https://sokalens.com/unsubscribe?token=abc"})
	if _, err := (ResendTransport{APIKey: "key", BaseURL: server.URL, From: "notifications@sokalens.com", Client: server.Client()}).
		Send(context.Background(), DeliveryRequest{Message: Message{Payload: payload}, IdempotencyKey: "digest-1"}); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	headers, ok := received["headers"].(map[string]any)
	if !ok || headers["List-Unsubscribe"] != "<https://sokalens.com/unsubscribe?token=abc>" {
		t.Fatalf("request headers = %v", received["headers"])
	}
}

// Cloudflare's header support is unverified for this account, so it must
// refuse rather than drop the header silently.
func TestCloudflareRefusesUnsubscribeHeadersUntilVerified(t *testing.T) {
	payload, _ := json.Marshal(EmailPayload{To: "fan@example.com", Subject: "Your week", Text: "...", ListUnsubscribe: "https://sokalens.com/unsubscribe?token=abc"})
	_, err := (CloudflareTransport{AccountID: "acct", APIToken: "token", From: "notifications@sokalens.com"}).
		Send(context.Background(), DeliveryRequest{Message: Message{Payload: payload}, IdempotencyKey: "digest-1"})
	if FaultOf(err) != FaultUnavailable {
		t.Fatalf("err = %v, fault = %q", err, FaultOf(err))
	}
}

func TestRuntimeTransportRoutesBulkPastAProviderThatCannotCarryHeaders(t *testing.T) {
	cloudflare, resend := 0, 0
	runtime := RuntimeTransport{
		DefaultProvider: "cloudflare",
		Fallbacks:       []string{"resend"},
		Providers: map[string]Transport{
			// Ready and healthy -- it simply cannot carry the header this
			// message must have.
			"cloudflare": CloudflareTransport{AccountID: "acct", APIToken: "token", From: "notifications@sokalens.com", Client: countingClient(&cloudflare)},
			"resend":     headerCapableTransport{calls: &resend},
		},
	}
	delivery, err := runtime.Send(context.Background(), DeliveryRequest{
		Message: bulkMessage(t, EmailPayload{To: "fan@example.com", Subject: "Your week", Text: "...", ListUnsubscribe: "https://sokalens.com/unsubscribe?token=abc"}),
	})
	if err != nil || delivery.Provider != "resend" {
		t.Fatalf("delivery = %+v, err = %v", delivery, err)
	}
	if cloudflare != 0 || resend != 1 {
		t.Fatalf("cloudflare=%d resend=%d", cloudflare, resend)
	}
}

type headerCapableTransport struct{ calls *int }

func (h headerCapableTransport) Capabilities() Capability { return CapabilityCustomHeaders }

func (h headerCapableTransport) Send(context.Context, DeliveryRequest) (Delivery, error) {
	*h.calls++
	return Delivery{ProviderMessageID: "resend:ok"}, nil
}

// countingClient fails loudly if it is ever used: the capability filter should
// mean Cloudflare is never called for a message it cannot carry.
func countingClient(calls *int) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		*calls++
		return nil, http.ErrNotSupported
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
