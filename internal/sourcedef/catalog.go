package sourcedef

import (
	"embed"
	"fmt"
	"io/fs"

	"gopkg.in/yaml.v3"
)

// catalogFS embeds every provider definition so the built binary carries the
// catalog with no files on disk (same single-binary rationale as migrations).
//
//go:embed catalog/*.yaml
var catalogFS embed.FS

// Load parses and validates every embedded source definition, returning them
// keyed by slug. It fails loudly on a malformed or duplicate definition so a
// bad catalog entry is caught at boot/test time, not at ingest time.
func Load() (map[string]Definition, error) {
	paths, err := fs.Glob(catalogFS, "catalog/*.yaml")
	if err != nil {
		return nil, fmt.Errorf("scanning catalog: %w", err)
	}

	defs := make(map[string]Definition, len(paths))
	for _, path := range paths {
		data, err := catalogFS.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}

		var def Definition
		if err := yaml.Unmarshal(data, &def); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		if err := def.validate(); err != nil {
			return nil, fmt.Errorf("invalid definition %s: %w", path, err)
		}
		if _, dup := defs[def.Slug]; dup {
			return nil, fmt.Errorf("duplicate slug %q (in %s)", def.Slug, path)
		}
		defs[def.Slug] = def
	}
	return defs, nil
}

func (d Definition) validate() error {
	if d.Slug == "" {
		return fmt.Errorf("slug is required")
	}
	if d.Name == "" {
		return fmt.Errorf("name is required")
	}
	switch d.Verification.Type {
	case "none":
		return nil // no signature to verify
	case "hmac":
		return d.Verification.validateHMAC()
	case "api_key":
		if d.Verification.SignatureHeader == "" {
			return fmt.Errorf("verification.signature_header is required for api_key")
		}
		return nil
	case "basic_auth":
		return nil // credentials are the secret; header is always Authorization
	default:
		return fmt.Errorf("verification.type %q is invalid (want: hmac, api_key, basic_auth, none)", d.Verification.Type)
	}
}

func (v Verification) validateHMAC() error {
	switch v.Algorithm {
	case "sha256", "sha1":
	default:
		return fmt.Errorf("verification.algorithm %q is invalid (want: sha256, sha1)", v.Algorithm)
	}
	switch v.Encoding {
	case "hex", "base64":
	default:
		return fmt.Errorf("verification.encoding %q is invalid (want: hex, base64)", v.Encoding)
	}
	if v.SignatureHeader == "" {
		return fmt.Errorf("verification.signature_header is required for hmac")
	}
	if ts := v.Timestamp; ts != nil {
		if ts.TimestampField == "" || ts.SignatureField == "" {
			return fmt.Errorf("verification.timestamp requires timestamp_field and signature_field")
		}
		if ts.ToleranceSeconds <= 0 {
			return fmt.Errorf("verification.timestamp.tolerance_seconds must be positive")
		}
	}
	return nil
}
