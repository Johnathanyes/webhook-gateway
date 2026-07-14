package sourcedef

import "testing"

// TestLoad runs against the real embedded catalog, so a malformed or
// duplicate definition fails here (and in CI) instead of at ingest time.
func TestLoad(t *testing.T) {
	defs, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := defs["generic_hmac"]; !ok {
		t.Fatalf("expected generic_hmac in catalog; got %d definitions", len(defs))
	}
}
