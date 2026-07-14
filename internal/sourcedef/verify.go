package sourcedef

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Verify reports whether an inbound request satisfies a provider's
// verification scheme.
//
// A false result means the request did not verify — a missing, malformed, or
// mismatched signature, or a timestamp outside the tolerance window. That is a
// normal outcome, not an error: the event is still stored (with verified
// false). An error is returned only for a
// misconfigured definition, which Load's validation should already prevent.
//
// Every secret comparison goes through hmac.Equal / subtle.ConstantTimeCompare
// so a wrong signature can't be discovered a byte at a time via timing.
func Verify(def Definition, body []byte, headers http.Header, secret []byte) (bool, error) {
	switch def.Verification.Type {
	case "none":
		// The operator opted out of verification, so there is no signature
		// that could be invalid; the event is accepted as verified.
		return true, nil
	case "hmac":
		return verifyHMAC(def.Verification, body, headers, secret), nil
	case "api_key":
		return verifyAPIKey(def.Verification, headers, secret), nil
	case "basic_auth":
		return verifyBasicAuth(headers, secret), nil
	default:
		return false, fmt.Errorf("unknown verification type %q", def.Verification.Type)
	}
}

func verifyHMAC(v Verification, body []byte, headers http.Header, secret []byte) bool {
	raw := headers.Get(v.SignatureHeader)
	if raw == "" {
		return false
	}

	// Determine the signed payload and pull the encoded signature out of the
	// header, which differs between plain and timestamp-signed schemes.
	var payload []byte
	var provided string

	if v.Timestamp != nil {
		fields := parseSignatureFields(raw)
		tsStr := fields[v.Timestamp.TimestampField]
		provided = fields[v.Timestamp.SignatureField]
		if tsStr == "" || provided == "" {
			return false
		}
		ts, err := strconv.ParseInt(tsStr, 10, 64)
		if err != nil {
			return false
		}
		// Reject timestamps outside the tolerance window in either direction,
		// so an old (once-valid) request can't be replayed.
		delta := time.Now().Unix() - ts
		if delta < 0 {
			delta = -delta
		}
		if delta > int64(v.Timestamp.ToleranceSeconds) {
			return false
		}
		payload = []byte(tsStr + "." + string(body))
	} else {
		provided = strings.TrimPrefix(raw, v.SignaturePrefix)
		payload = body
	}

	sig, err := decodeSignature(provided, v.Encoding)
	if err != nil {
		return false
	}

	expected := computeHMAC(v.Algorithm, secret, payload)
	return hmac.Equal(expected, sig)
}

func verifyAPIKey(v Verification, headers http.Header, secret []byte) bool {
	provided := headers.Get(v.SignatureHeader)
	if provided == "" {
		return false
	}
	// Strip a scheme prefix like "Bearer " when the definition declares one.
	// The prefix itself isn't secret, so a plain trim is fine; only the key
	// comparison below needs to be constant-time.
	provided = strings.TrimPrefix(provided, v.SignaturePrefix)
	return subtle.ConstantTimeCompare([]byte(provided), secret) == 1
}

func verifyBasicAuth(headers http.Header, secret []byte) bool {
	const prefix = "Basic "
	auth := headers.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, prefix))
	if err != nil {
		return false
	}
	// The secret is the expected "username:password" credential string. It
	// arrives here already decrypted from sources.signing_secret_encrypted
	// (AES-256-GCM, BR-26) — machine credentials for HMAC-family schemes must
	// be recoverable to verify at all, so basic_auth shares that encrypted
	// storage rather than a hash like users.password_hash.
	return subtle.ConstantTimeCompare(decoded, secret) == 1
}

// parseSignatureFields splits a Stripe-style header ("t=123,v1=abc,v0=def")
// into its named fields. Extra or unknown elements are ignored.
func parseSignatureFields(s string) map[string]string {
	out := make(map[string]string)
	for part := range strings.SplitSeq(s, ",") {
		if k, val, ok := strings.Cut(part, "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(val)
		}
	}
	return out
}

func decodeSignature(s, encoding string) ([]byte, error) {
	switch encoding {
	case "hex":
		return hex.DecodeString(s)
	case "base64":
		return base64.StdEncoding.DecodeString(s)
	default:
		return nil, fmt.Errorf("unknown encoding %q", encoding)
	}
}

func computeHMAC(algorithm string, secret, payload []byte) []byte {
	var newHash func() hash.Hash
	switch algorithm {
	case "sha256":
		newHash = sha256.New
	case "sha1":
		newHash = sha1.New
	default:
		return nil // unreachable: algorithm is validated at load time
	}
	mac := hmac.New(newHash, secret)
	mac.Write(payload)
	return mac.Sum(nil)
}
