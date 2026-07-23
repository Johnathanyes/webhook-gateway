package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// How many characters are shown so user can recognize key
const KeyPrefixLen = 12

const keyScheme = "whg_"

// Generate a 32 bit api key with a prefix from keyScheme. Create a hash to be stored instead
// of storing key
func GenerateAPIKey() (plaintext, hash, prefix string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", fmt.Errorf("generating API keyL %w", err)
	}

	plaintext = keyScheme + base64.RawURLEncoding.EncodeToString(raw)
		return plaintext, HashAPIKey(plaintext), plaintext[:KeyPrefixLen], nil
}

// Returns the hex-encoded SHA-256 of a plaintext key
func HashAPIKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// Reports if input looks like an actual api key
func LooksLikeAPIKey(token string) bool {
	return strings.HasPrefix(token, keyScheme)
}