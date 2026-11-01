package notifications

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"testing"
)

func TestRepositoryDecryptsSensitivePayloadEnvelope(t *testing.T) {
	secret := "application-secret"
	plaintext := []byte(`{"to":"fan@example.com","subject":"Reset","text":"one-time link"}`)
	envelope := encryptedEnvelopeForTest(t, secret, plaintext)
	decrypted, err := NewRepository(nil).WithPayloadSecret(secret).decryptPayload(envelope)
	if err != nil {
		t.Fatalf("decryptPayload: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("decrypted payload = %s", decrypted)
	}
	if _, err := NewRepository(nil).WithPayloadSecret("wrong-secret").decryptPayload(envelope); err == nil {
		t.Fatal("expected wrong key to fail authentication")
	}
}

func encryptedEnvelopeForTest(t *testing.T, secret string, plaintext []byte) []byte {
	t.Helper()
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatal(err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, []byte("sokalens-notification-v1"))
	payload, err := json.Marshal(map[string]any{"encrypted": true, "version": 1, "nonce": base64.RawStdEncoding.EncodeToString(nonce), "ciphertext": base64.RawStdEncoding.EncodeToString(ciphertext)})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
