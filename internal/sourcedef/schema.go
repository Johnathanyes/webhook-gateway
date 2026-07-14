package sourcedef

// Definition is one entry in the source-definition catalog: a declarative,
// data-only description of how to verify webhooks from a given provider.
// The point of this format is that a new provider can be added by
// dropping a YAML file in catalog/
type Definition struct {
	// Slug is the stable identifier stored in sources.provider_type and used
	// to look a definition up at ingest time. Must be unique in the catalog.
	Slug        string `yaml:"slug"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`

	Verification Verification `yaml:"verification"`
}

// Verification describes the signature scheme for a provider. It covers the
// HMAC family (generic HMAC, GitHub, Stripe, Shopify, ...) plus two simpler
// shared-secret schemes, basic_auth and api_key.
type Verification struct {
	// Type selects the scheme:
	//   "hmac"       — HMAC of the raw body (optionally timestamp-signed)
	//   "api_key"    — static key compared against a header value
	//   "basic_auth" — HTTP Basic credentials compared against the secret
	//   "none"       — no verification
	Type string `yaml:"type"`

	// Algorithm and Encoding apply only when Type == "hmac".
	Algorithm string `yaml:"algorithm"` // "sha256" | "sha1"
	Encoding  string `yaml:"encoding"`  // "hex" | "base64"

	// SignatureHeader is the header the signature (hmac) or key (api_key) is
	// read from, e.g. "X-Hub-Signature-256" or "X-API-Key". basic_auth always
	// reads the standard "Authorization" header, so it ignores this.
	SignatureHeader string `yaml:"signature_header"`

	// SignaturePrefix, when set, is stripped from the header value before
	// comparison. GitHub sends "sha256=<hex>" in X-Hub-Signature-256 (prefix
	// "sha256="); a Bearer-style api_key sends "Bearer <key>" in its header
	// (prefix "Bearer "). (hmac and api_key.)
	SignaturePrefix string `yaml:"signature_prefix"`

	// Timestamp, when set, selects the timestamp-signed HMAC scheme
	// (Stripe-style): SignatureHeader holds comma-separated key=value pairs,
	// the signed payload is "<timestamp>.<body>", and the timestamp must be
	// within the tolerance of now. (hmac only.)
	Timestamp *TimestampScheme `yaml:"timestamp"`
}

// TimestampScheme configures Stripe-style timestamp-signed HMAC, where the
// signature header carries both a unix timestamp and the signature as named
// fields (e.g. "t=1690000000,v1=abc...") and the timestamp guards against
// replay of an old, once-valid request.
type TimestampScheme struct {
	TimestampField   string `yaml:"timestamp_field"`   // field holding the unix timestamp, e.g. "t"
	SignatureField   string `yaml:"signature_field"`   // field holding the signature, e.g. "v1"
	ToleranceSeconds int    `yaml:"tolerance_seconds"` // reject when |now - timestamp| exceeds this
}
