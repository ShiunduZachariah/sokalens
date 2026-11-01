package admin

import (
	"crypto/sha256"
	"encoding/base32"
	"testing"
	"time"
)

func TestMFASecretEncryptionRoundTrip(t *testing.T) {
	key := sha256.Sum256([]byte("test-encryption-key"))
	ciphertext, err := encryptMFASecret(key, "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decryptMFASecret(key, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("plain = %q", plain)
	}
}

func TestValidateTOTPAcceptsCurrentWindowOnly(t *testing.T) {
	secret := "JBSWY3DPEHPK3PXP"
	decoded, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	now := time.Unix(1_700_000_000, 0).UTC()
	code := totpCode(decoded, now.Unix()/30)
	if !validateTOTP(secret, code, now) {
		t.Fatal("current code rejected")
	}
	if validateTOTP(secret, "000000", now) {
		t.Fatal("invalid code accepted")
	}
	if validateTOTP(secret, code, now.Add(2*time.Minute)) {
		t.Fatal("stale code accepted")
	}
}
