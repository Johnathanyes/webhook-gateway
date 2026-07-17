package sourcedef_test

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"net/http"
	"strconv"
	"testing"
	"time"

	sourcedef "webhook-gateway/internal/sourcedef"
)

// mac computes an HMAC independently of the engine, so the tests act as known
// vectors the engine must reproduce rather than re-running its own code.
func mac(algorithm string, secret, payload []byte) []byte {
	var newHash func() hash.Hash
	switch algorithm {
	case "sha256":
		newHash = sha256.New
	case "sha1":
		newHash = sha1.New
	default:
		panic("unknown algorithm " + algorithm)
	}
	m := hmac.New(newHash, secret)
	m.Write(payload)
	return m.Sum(nil)
}

func encode(b []byte, encoding string) string {
	switch encoding {
	case "hex":
		return hex.EncodeToString(b)
	case "base64":
		return base64.StdEncoding.EncodeToString(b)
	default:
		panic("unknown encoding " + encoding)
	}
}

func header(name, value string) http.Header {
	h := http.Header{}
	h.Set(name, value)
	return h
}

// TestVerifyHMAC covers every algorithm × encoding combination, plus the
// failure paths that matter: tampered body, wrong secret, and missing header.
func TestVerifyHMAC(t *testing.T) {
	const sigHeader = "X-Signature"
	secret := []byte("topsecret")
	body := []byte(`{"hello":"world"}`)

	for _, algorithm := range []string{"sha256", "sha1"} {
		for _, encoding := range []string{"hex", "base64"} {
			t.Run(fmt.Sprintf("%s/%s", algorithm, encoding), func(t *testing.T) {
				def := sourcedef.Definition{
					Slug: "generic", Name: "Generic",
					Verification: sourcedef.Verification{
						Type:            "hmac",
						Algorithm:       algorithm,
						Encoding:        encoding,
						SignatureHeader: sigHeader,
					},
				}
				sig := encode(mac(algorithm, secret, body), encoding)

				ok, err := sourcedef.Verify(def, body, header(sigHeader, sig), secret)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !ok {
					t.Error("valid signature rejected")
				}

				if ok, _ := sourcedef.Verify(def, []byte("tampered"), header(sigHeader, sig), secret); ok {
					t.Error("tampered body accepted")
				}
				if ok, _ := sourcedef.Verify(def, body, header(sigHeader, sig), []byte("wrong-secret")); ok {
					t.Error("wrong secret accepted")
				}
				if ok, _ := sourcedef.Verify(def, body, http.Header{}, secret); ok {
					t.Error("missing signature header accepted")
				}
			})
		}
	}
}

// TestVerifyHMACPrefix is the GitHub shape: the signature is carried as
// "sha256=<hex>" and the prefix must be stripped before decoding.
func TestVerifyHMACPrefix(t *testing.T) {
	const sigHeader = "X-Hub-Signature-256"
	secret := []byte("github-secret")
	body := []byte(`{"action":"opened"}`)

	def := sourcedef.Definition{
		Slug: "github", Name: "GitHub",
		Verification: sourcedef.Verification{
			Type:            "hmac",
			Algorithm:       "sha256",
			Encoding:        "hex",
			SignatureHeader: sigHeader,
			SignaturePrefix: "sha256=",
		},
	}
	sig := "sha256=" + hex.EncodeToString(mac("sha256", secret, body))

	ok, err := sourcedef.Verify(def, body, header(sigHeader, sig), secret)
	if err != nil || !ok {
		t.Fatalf("prefixed signature rejected: ok=%v err=%v", ok, err)
	}
	if ok, _ := sourcedef.Verify(def, []byte("tampered"), header(sigHeader, sig), secret); ok {
		t.Error("tampered body accepted")
	}
}

// TestVerifyHMACTimestamp is the Stripe shape: "t=<ts>,v1=<hex>" over the
// payload "<ts>.<body>", with a tolerance window guarding against replay.
func TestVerifyHMACTimestamp(t *testing.T) {
	const sigHeader = "Stripe-Signature"
	secret := []byte("whsec")
	body := []byte(`{"id":"evt_1"}`)

	def := sourcedef.Definition{
		Slug: "stripe", Name: "Stripe",
		Verification: sourcedef.Verification{
			Type:            "hmac",
			Algorithm:       "sha256",
			Encoding:        "hex",
			SignatureHeader: sigHeader,
			Timestamp: &sourcedef.TimestampScheme{
				TimestampField:   "t",
				SignatureField:   "v1",
				ToleranceSeconds: 300,
			},
		},
	}

	sign := func(ts int64) http.Header {
		payload := []byte(strconv.FormatInt(ts, 10) + "." + string(body))
		sig := hex.EncodeToString(mac("sha256", secret, payload))
		return header(sigHeader, fmt.Sprintf("t=%d,v1=%s", ts, sig))
	}

	ok, err := sourcedef.Verify(def, body, sign(time.Now().Unix()), secret)
	if err != nil || !ok {
		t.Errorf("valid timestamped signature rejected: ok=%v err=%v", ok, err)
	}
	if ok, _ := sourcedef.Verify(def, body, sign(time.Now().Unix()-3600), secret); ok {
		t.Error("expired timestamp accepted")
	}
	if ok, _ := sourcedef.Verify(def, []byte("tampered"), sign(time.Now().Unix()), secret); ok {
		t.Error("tampered body with valid timestamp accepted")
	}
}

func TestVerifyAPIKey(t *testing.T) {
	const sigHeader = "X-API-Key"
	secret := []byte("secret-key-value")
	def := sourcedef.Definition{
		Slug: "apikey", Name: "API Key",
		Verification: sourcedef.Verification{Type: "api_key", SignatureHeader: sigHeader},
	}

	if ok, err := sourcedef.Verify(def, nil, header(sigHeader, "secret-key-value"), secret); err != nil || !ok {
		t.Fatalf("valid api key rejected: ok=%v err=%v", ok, err)
	}
	if ok, _ := sourcedef.Verify(def, nil, header(sigHeader, "wrong"), secret); ok {
		t.Error("wrong api key accepted")
	}
	if ok, _ := sourcedef.Verify(def, nil, http.Header{}, secret); ok {
		t.Error("missing api key accepted")
	}
}

// TestVerifyAPIKeyBearer covers the "Authorization: Bearer <key>" shape via
// signature_prefix, the same mechanism GitHub's "sha256=" uses for hmac.
func TestVerifyAPIKeyBearer(t *testing.T) {
	secret := []byte("secret-key-value")
	def := sourcedef.Definition{
		Slug: "bearer", Name: "Bearer",
		Verification: sourcedef.Verification{
			Type:            "api_key",
			SignatureHeader: "Authorization",
			SignaturePrefix: "Bearer ",
		},
	}

	if ok, err := sourcedef.Verify(def, nil, header("Authorization", "Bearer secret-key-value"), secret); err != nil || !ok {
		t.Fatalf("valid bearer key rejected: ok=%v err=%v", ok, err)
	}
	if ok, _ := sourcedef.Verify(def, nil, header("Authorization", "Bearer wrong"), secret); ok {
		t.Error("wrong bearer key accepted")
	}
}

func TestVerifyBasicAuth(t *testing.T) {
	secret := []byte("user:pass")
	def := sourcedef.Definition{
		Slug: "basic", Name: "Basic",
		Verification: sourcedef.Verification{Type: "basic_auth"},
	}

	valid := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
	if ok, err := sourcedef.Verify(def, nil, header("Authorization", valid), secret); err != nil || !ok {
		t.Fatalf("valid basic auth rejected: ok=%v err=%v", ok, err)
	}

	wrong := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:wrong"))
	if ok, _ := sourcedef.Verify(def, nil, header("Authorization", wrong), secret); ok {
		t.Error("wrong basic auth credentials accepted")
	}
	if ok, _ := sourcedef.Verify(def, nil, http.Header{}, secret); ok {
		t.Error("missing Authorization header accepted")
	}
}

func TestVerifyNone(t *testing.T) {
	def := sourcedef.Definition{
		Slug: "none", Name: "None",
		Verification: sourcedef.Verification{Type: "none"},
	}
	ok, err := sourcedef.Verify(def, []byte("anything"), http.Header{}, nil)
	if err != nil || !ok {
		t.Fatalf("none should verify true: ok=%v err=%v", ok, err)
	}
}
