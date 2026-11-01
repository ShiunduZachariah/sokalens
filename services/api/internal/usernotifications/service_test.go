package usernotifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestSensitivePayloadEncryptionRequiresSecretAndHidesToken(t *testing.T) {
	plaintext := []byte(`{"to":"fan@example.com","text":"https://example.test/reset?token=raw-secret-token"}`)
	if _, err := NewService(nil).encryptSensitivePayload(plaintext); err == nil {
		t.Fatal("expected missing encryption key to fail closed")
	}
	encrypted, err := NewService(nil, "application-secret").encryptSensitivePayload(plaintext)
	if err != nil {
		t.Fatalf("encryptSensitivePayload: %v", err)
	}
	if bytes.Contains(encrypted, []byte("raw-secret-token")) || bytes.Contains(encrypted, []byte("fan@example.com")) {
		t.Fatalf("encrypted envelope exposes sensitive plaintext: %s", encrypted)
	}
	var envelope struct {
		Encrypted bool `json:"encrypted"`
		Version   int  `json:"version"`
	}
	if err := json.Unmarshal(encrypted, &envelope); err != nil || !envelope.Encrypted || envelope.Version != 1 {
		t.Fatalf("envelope = %+v err=%v", envelope, err)
	}
}

func TestOnlyTransactionalAccountEmailCanBeQueuedDuringLaunch(t *testing.T) {
	service := NewService(nil)
	for _, category := range []string{"email_verification", "password_reset"} {
		if err := service.QueueEmailHTML(context.Background(), 1, category, "dedupe", "fan@example.test", "Subject", "Body", "", ""); errors.Is(err, ErrDeliveryDormant) {
			t.Fatalf("transactional category %q was treated as dormant", category)
		}
	}
	for _, category := range []string{"welcome", "password_change", "login", "favorites", "predictions", "digest", "marketing"} {
		err := service.QueueEmailHTML(context.Background(), 1, category, "dedupe", "fan@example.test", "Subject", "Body", "", "")
		if !errors.Is(err, ErrDeliveryDormant) {
			t.Fatalf("category %q error=%v", category, err)
		}
	}
}
