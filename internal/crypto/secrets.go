// Package crypto encrypts provider signing secrets at rest with AES-256-GCM
// (BR-26). GCM is an AEAD, so decryption is authenticated: a tampered
// ciphertext or a wrong key fails closed rather than returning garbage.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

// CurrentKeyVersion is the key version new ciphertexts are written with. It is
// stored per row in sources.signing_secret_key_version so that introducing a
// second key later (rotation) is an additive change, not a migration.
const CurrentKeyVersion = 1

// Encryptor seals and opens secrets with a single AES-256-GCM key.
type Encryptor struct {
	aead    cipher.AEAD
	version int
}

// NewEncryptor builds an Encryptor from the base64-encoded master key (the
// ENCRYPTION_KEY config value). It decodes and re-checks the length here, at
// the boundary that actually uses the key, so a 32-byte AES-256 key is
// guaranteed regardless of how the key reached us.
func NewEncryptor(base64Key string) (*Encryptor, error) {
	key, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil {
		return nil, fmt.Errorf("decoding encryption key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes, got %d", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	return &Encryptor{aead: aead, version: CurrentKeyVersion}, nil
}

// Encrypt seals plaintext and returns nonce||ciphertext||tag together with the
// key version it was sealed under. The nonce is fresh from crypto/rand on every
// call. 
func (e *Encryptor) Encrypt(plaintext []byte) (ciphertext []byte, keyVersion int, err error) {
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, 0, fmt.Errorf("generating nonce: %w", err)
	}
	// Seal appends the ciphertext+tag to its first argument, so passing the
	// nonce as the destination prefixes it — output is nonce||ciphertext||tag.
	return e.aead.Seal(nonce, nonce, plaintext, nil), e.version, nil
}

func (e *Encryptor) Decrypt(ciphertext []byte) ([]byte, error) {
	ns := e.aead.NonceSize()
	if len(ciphertext) < ns {
		return nil, fmt.Errorf("ciphertext too short: %d bytes", len(ciphertext))
	}
	nonce, sealed := ciphertext[:ns], ciphertext[ns:]

	plaintext, err := e.aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypting secret")
	}
	return plaintext, nil
}
