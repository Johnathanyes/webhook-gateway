package crypto_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"webhook-gateway/internal/crypto"
)

// newKey returns a fresh base64-encoded 32-byte key, the shape NewEncryptor
// expects from the ENCRYPTION_KEY config value.
func newKey(t *testing.T) string {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("generating key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

func newEncryptor(t *testing.T) *crypto.Encryptor {
	t.Helper()
	e, err := crypto.NewEncryptor(newKey(t))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	return e
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	e := newEncryptor(t)
	secret := []byte("whsec_super_secret_signing_key")

	ct, version, err := e.Encrypt(secret)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if version != crypto.CurrentKeyVersion {
		t.Errorf("key version = %d, want %d", version, crypto.CurrentKeyVersion)
	}
	if bytes.Contains(ct, secret) {
		t.Error("ciphertext contains the plaintext secret")
	}

	got, err := e.Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Errorf("round-trip = %q, want %q", got, secret)
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	enc := newEncryptor(t)
	ct, _, err := enc.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	other := newEncryptor(t) // different random key
	if _, err := other.Decrypt(ct); err == nil {
		t.Error("Decrypt with wrong key succeeded, want authentication failure")
	}
}

func TestDecryptTamperedCiphertextFails(t *testing.T) {
	e := newEncryptor(t)
	ct, _, err := e.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Flip a bit in the last byte (inside the GCM tag / ciphertext).
	ct[len(ct)-1] ^= 0x01
	if _, err := e.Decrypt(ct); err == nil {
		t.Error("Decrypt of tampered ciphertext succeeded, want authentication failure")
	}
}

func TestEncryptUsesFreshNonce(t *testing.T) {
	e := newEncryptor(t)
	secret := []byte("secret")

	ct1, _, err := e.Encrypt(secret)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	ct2, _, err := e.Encrypt(secret)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Equal(ct1, ct2) {
		t.Error("encrypting the same plaintext twice produced identical ciphertext; nonce is not fresh")
	}
}

func TestDecryptTooShort(t *testing.T) {
	e := newEncryptor(t)
	if _, err := e.Decrypt([]byte("short")); err == nil {
		t.Error("Decrypt of too-short input succeeded, want error")
	}
}

func TestNewEncryptorRejectsBadKey(t *testing.T) {
	tests := map[string]string{
		"not base64":    "not!valid!base64!",
		"wrong length":  base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"empty":         "",
	}
	for name, key := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := crypto.NewEncryptor(key); err == nil {
				t.Errorf("NewEncryptor(%q) succeeded, want error", key)
			}
		})
	}
}
