package auth

import (
	"strings"
	"testing"
)

func TestGenerateAPIKey(t *testing.T) {
	plaintext, hash, prefix, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(plaintext, "whg_") {
		t.Errorf("plaintext %q does not start with whg_", plaintext)
	}
	// "whg_" (4) + base64url of 32 bytes (43 chars, no padding)
	if len(plaintext) != 47 {
		t.Errorf("plaintext length = %d, want 47", len(plaintext))
	}
	if prefix != plaintext[:KeyPrefixLen] {
		t.Errorf("prefix %q is not the first %d chars of the key", prefix, KeyPrefixLen)
	}
	if hash != HashAPIKey(plaintext) {
		t.Error("returned hash does not match HashAPIKey(plaintext)")
	}
	// hex sha256
	if len(hash) != 64 {
		t.Errorf("hash length = %d, want 64", len(hash))
	}
}

func TestGenerateAPIKeyUnique(t *testing.T) {
	a, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	b, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two generated keys are identical")
	}
}

func TestLooksLikeAPIKey(t *testing.T) {
	if !LooksLikeAPIKey("whg_abc123") {
		t.Error("whg_-prefixed token should look like an API key")
	}
	if LooksLikeAPIKey("hunter2") {
		t.Error("admin password should not look like an API key")
	}
}
