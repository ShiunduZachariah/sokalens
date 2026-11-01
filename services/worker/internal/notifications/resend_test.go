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

func TestResendTransportUsesServerSideCredentialsAndIdempotency(t *testing.T) {
	var receivedAuthorization string
	var receivedIdempotency string
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthorization = r.Header.Get("Authorization")
		receivedIdempotency = r.Header.Get("Idempotency-Key")
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"email_123"}`))
	}))
	defer server.Close()

	transport := ResendTransport{APIKey: "test-server-key", BaseURL: server.URL, From: "alerts@sokalens.test", FromName: "SokaLens"}
	delivery, err := transport.Send(context.Background(), DeliveryRequest{
		Message:        Message{Payload: []byte(`{"to":"fan@example.com","subject":"Prediction ready","text":"Plain message","html":"<p>HTML message</p>"}`)},
		IdempotencyKey: "prediction-42-ready",
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if delivery.ProviderMessageID != "resend:email_123" || delivery.ResponseCode != "201" {
		t.Fatalf("delivery = %+v", delivery)
	}
	if receivedAuthorization != "Bearer test-server-key" || receivedIdempotency != "prediction-42-ready" {
		t.Fatalf("headers authorization=%q idempotency=%q", receivedAuthorization, receivedIdempotency)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(receivedBody), &decoded); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if decoded["from"] != "SokaLens <alerts@sokalens.test>" || decoded["text"] != "Plain message" || decoded["html"] != "<p>HTML message</p>" {
		t.Fatalf("request body = %#v", decoded)
	}
}

func TestResendTransportReturnsSafeStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "sensitive upstream response", http.StatusUnauthorized)
	}))
	defer server.Close()
	transport := ResendTransport{APIKey: "test-server-key", BaseURL: server.URL, From: "alerts@sokalens.test"}
	_, err := transport.Send(context.Background(), DeliveryRequest{Message: Message{Payload: []byte(`{"to":"fan@example.com","subject":"Test","text":"Message"}`)}})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "test-server-key") {
		t.Fatalf("error = %v", err)
	}
}
