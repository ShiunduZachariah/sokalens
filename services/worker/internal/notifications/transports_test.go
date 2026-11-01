package notifications

import (
	"context"
	"errors"
	"net/smtp"
	"strings"
	"testing"
)

func TestSMTPTransportUsesOutboxDedupeKeyAsMessageID(t *testing.T) {
	var body string
	transport := SMTPTransport{
		Address: "mailhog:1025",
		From:    "alerts@sokalens.local",
		SendMail: func(_ string, _ smtp.Auth, _ string, _ []string, message []byte) error {
			body = string(message)
			return nil
		},
	}
	delivery, err := transport.Send(context.Background(), DeliveryRequest{
		Message:        Message{Payload: []byte(`{"to":"fan@example.com","subject":"Match update","text":"Kickoff soon","html":"<p>Kickoff soon</p>","request_id":"request-42"}`)},
		IdempotencyKey: "match-42-start",
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if delivery.ProviderMessageID != "smtp:match-42-start" || !strings.Contains(body, "Message-ID: <match-42-start@sokalens.local>") || !strings.Contains(body, "multipart/alternative") || !strings.Contains(body, "X-SokaLens-Request-ID: request-42") || !strings.Contains(body, "<p>Kickoff soon</p>") {
		t.Fatalf("delivery=%+v body=%q", delivery, body)
	}
}

func TestSMTPAcceptedDataDoesNotRetryWhenQuitFails(t *testing.T) {
	closed := false
	quit := false
	err := completeSMTPDelivery(func() error {
		closed = true
		return nil
	}, func() error {
		quit = true
		return errors.New("connection closed after acceptance")
	})
	if err != nil || !closed || !quit {
		t.Fatalf("err=%v closed=%v quit=%v", err, closed, quit)
	}
}

func TestSMTPDataCloseFailureRemainsRetryable(t *testing.T) {
	want := errors.New("message not accepted")
	err := completeSMTPDelivery(func() error { return want }, func() error {
		t.Fatal("QUIT called after DATA close failed")
		return nil
	})
	if !errors.Is(err, want) {
		t.Fatalf("err=%v, want %v", err, want)
	}
}

func TestSMTPTransportRejectsHeaderInjection(t *testing.T) {
	transport := SMTPTransport{
		Address: "mailhog:1025",
		From:    "alerts@sokalens.local",
		SendMail: func(_ string, _ smtp.Auth, _ string, _ []string, _ []byte) error {
			t.Fatal("SendMail called for an injected header")
			return nil
		},
	}
	_, err := transport.Send(context.Background(), DeliveryRequest{
		Message:        Message{Payload: []byte(`{"to":"fan@example.com\r\nBcc:evil@example.com","subject":"Safe\r\nBcc:evil@example.com","text":"hello"}`)},
		IdempotencyKey: "safe",
	})
	if err == nil || !strings.Contains(err.Error(), "newlines") {
		t.Fatalf("Send error=%v, want newline rejection", err)
	}
}
