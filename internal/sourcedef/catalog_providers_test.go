package sourcedef_test

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	sourcedef "webhook-gateway/internal/sourcedef"
)

// loadDef pulls one definition out of the real embedded catalog, so these
// tests exercise the actual YAML files end-to-end: Load() parsing +
// validation, then the verifier engine — zero provider-specific Go code.
func loadDef(t *testing.T, slug string) sourcedef.Definition {
	t.Helper()
	defs, err := sourcedef.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	def, ok := defs[slug]
	if !ok {
		t.Fatalf("catalog has no %q definition", slug)
	}
	return def
}

// TestGitHubCatalogEntry uses the exact example from GitHub's own docs
// (https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries):
// secret "It's a Secret to Everybody" + payload "Hello, World!" must yield
// X-Hub-Signature-256: sha256=757107ea...
func TestGitHubCatalogEntry(t *testing.T) {
	def := loadDef(t, "github")
	secret := []byte("It's a Secret to Everybody")
	body := []byte("Hello, World!")
	const docSig = "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"

	ok, err := sourcedef.Verify(def, body, header("X-Hub-Signature-256", docSig), secret)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Error("GitHub's documented example signature rejected")
	}

	if ok, _ := sourcedef.Verify(def, []byte("Hello, World"), header("X-Hub-Signature-256", docSig), secret); ok {
		t.Error("tampered body accepted")
	}
	if ok, _ := sourcedef.Verify(def, body, http.Header{}, secret); ok {
		t.Error("missing signature header accepted")
	}
}

// TestStripeCatalogEntry constructs signatures exactly per Stripe's manual-
// verification docs (https://docs.stripe.com/webhooks#verify-manually):
// HMAC-SHA256 over "<timestamp>.<body>", hex, in Stripe-Signature as t/v1
// fields. Stripe publishes the scheme but no fixed secret+payload vector,
// so the signing here follows their spec rather than a doc constant.
func TestStripeCatalogEntry(t *testing.T) {
	def := loadDef(t, "stripe")
	secret := []byte("whsec_test_secret")
	body := []byte(`{"id": "evt_test_webhook", "object": "event"}`)

	sign := func(ts int64) http.Header {
		payload := strconv.FormatInt(ts, 10) + "." + string(body)
		sig := encode(mac("sha256", secret, []byte(payload)), "hex")
		// Stripe sends extra fields (v0 on some accounts); they must be ignored.
		return header("Stripe-Signature", fmt.Sprintf("t=%d,v1=%s,v0=deadbeef", ts, sig))
	}

	ok, err := sourcedef.Verify(def, body, sign(time.Now().Unix()), secret)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Error("valid Stripe-style signature rejected")
	}

	if ok, _ := sourcedef.Verify(def, body, sign(time.Now().Unix()-3600), secret); ok {
		t.Error("timestamp beyond 5-minute tolerance accepted")
	}
	if ok, _ := sourcedef.Verify(def, []byte(`{"id": "evt_forged"}`), sign(time.Now().Unix()), secret); ok {
		t.Error("tampered body accepted")
	}
	if ok, _ := sourcedef.Verify(def, body, sign(time.Now().Unix()), []byte("wrong-secret")); ok {
		t.Error("wrong secret accepted")
	}
}
