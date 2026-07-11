package sourcedef

// Definition is one entry in the source-definition catalog: a declarative,
// data-only description of how to verify webhooks from a given provider.
// The point of this format (BR-37) is that a new provider can be added by
// dropping a YAML file in catalog/ — no Go code — so the community can grow
// the catalog toward 60+ providers via PRs that are pure data review.
type Definition struct {
	// Slug is the stable identifier stored in sources.provider_type and used
	// to look a definition up at ingest time. Must be unique in the catalog.
	Slug        string `yaml:"slug"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`

	Verification Verification `yaml:"verification"`
}

// Verification describes the signature scheme for a provider. v1 covers the
// HMAC family: generic HMAC, GitHub, Shopify, etc. are all "HMAC of the raw
// body, hex or base64, in header X". Timestamp-signed schemes (Stripe-style
// t=...,v1=...) and signature prefixes (GitHub's "sha256=") are the next
// fields to add here when provider-specific defs and the verifier engine
// land in Phase 1 — the schema stays intentionally small until something
// consumes more of it.
type Verification struct {
	// Type selects the scheme: "hmac" or "none" (no verification).
	Type string `yaml:"type"`

	// The following apply only when Type == "hmac".
	Algorithm       string `yaml:"algorithm"`        // "sha256" | "sha1"
	SignatureHeader string `yaml:"signature_header"` // header carrying the signature
	Encoding        string `yaml:"encoding"`         // "hex" | "base64"
}
