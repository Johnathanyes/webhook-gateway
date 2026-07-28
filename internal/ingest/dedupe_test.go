package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"webhook-gateway/internal/db"
)

func source(enabled bool, strategy, fieldPath string) db.Source {
	return db.Source{
		DedupeEnabled:   enabled,
		DedupeStrategy:  pgtype.Text{String: strategy, Valid: strategy != ""},
		DedupeFieldPath: pgtype.Text{String: fieldPath, Valid: fieldPath != ""},
	}
}

func TestDedupeKeyDisabledByDefault(t *testing.T) {
	body := []byte(`{"id":"evt_1"}`)
	if _, ok := dedupeKey(source(false, "", ""), body, body); ok {
		t.Error("a source with dedupe off produced a key")
	}
	// Even with a strategy configured, the enabled flag is what decides.
	if _, ok := dedupeKey(source(false, dedupeExact, ""), body, body); ok {
		t.Error("dedupe_enabled=false still produced a key")
	}
}

func TestDedupeKeyExactHashesTheRawBody(t *testing.T) {
	body := []byte(`{"id":"evt_1","amount":100}`)
	key, ok := dedupeKey(source(true, dedupeExact, ""), body, body)
	if !ok {
		t.Fatal("exact strategy produced no key")
	}
	sum := sha256.Sum256(body)
	if key != hex.EncodeToString(sum[:]) {
		t.Errorf("key = %q, want the sha256 of the raw body", key)
	}

	// Byte-identical bodies collide; anything else does not. Whitespace counts,
	// which is the point of "exact".
	same, _ := dedupeKey(source(true, dedupeExact, ""), body, body)
	if same != key {
		t.Error("identical bodies produced different keys")
	}
	spaced := []byte(`{"id":"evt_1", "amount":100}`)
	if other, _ := dedupeKey(source(true, dedupeExact, ""), spaced, spaced); other == key {
		t.Error("bodies differing in whitespace produced the same exact key")
	}

	// The raw body is hashed, not the parsed form: a body that isn't JSON at
	// all still dedupes.
	raw := []byte("<xml>not json</xml>")
	if _, ok := dedupeKey(source(true, dedupeExact, ""), raw, nil); !ok {
		t.Error("exact strategy needs no parsed body, but produced no key")
	}
}

func TestDedupeKeyFieldExtraction(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
		want string
	}{
		{"top level string", "$.id", `{"id":"evt_123"}`, "evt_123"},
		{"nested path", "$.data.object.id", `{"data":{"object":{"id":"pi_456"}}}`, "pi_456"},
		{"path without the $ prefix", "id", `{"id":"evt_789"}`, "evt_789"},
		{"integer id keeps its digits", "$.id", `{"id":12345678901234567890}`, "12345678901234567890"},
		{"decimal is not reformatted", "$.id", `{"id":1.50}`, "1.50"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			got, ok := dedupeKey(source(true, dedupeField, tt.path), body, body)
			if !ok {
				t.Fatalf("no key produced for %s", tt.body)
			}
			if got != tt.want {
				t.Errorf("key = %q, want %q", got, tt.want)
			}
		})
	}
}

// Anything that can't yield an identity fails open: no key, so the event is
// delivered normally rather than dropped or bucketed under an empty key.
func TestDedupeKeyFieldFailsOpen(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{"missing field", "$.id", `{"other":"value"}`},
		{"missing nested parent", "$.data.object.id", `{"data":{}}`},
		{"field is an object", "$.data", `{"data":{"id":"x"}}`},
		{"field is an array", "$.data", `{"data":[1,2]}`},
		{"field is null", "$.id", `{"id":null}`},
		{"field is empty string", "$.id", `{"id":""}`},
		{"field is a bool", "$.id", `{"id":true}`},
		{"body is not an object", "$.id", `[1,2,3]`},
		{"path is empty", "", `{"id":"evt_1"}`},
		{"traversing through a scalar", "$.id.nested", `{"id":"evt_1"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			if key, ok := dedupeKey(source(true, dedupeField, tt.path), body, body); ok {
				t.Errorf("got key %q, want no key (fail open)", key)
			}
		})
	}

	// A field strategy with no configured path, and an unparseable body, both
	// fail open too.
	if _, ok := dedupeKey(source(true, dedupeField, ""), []byte(`{"id":"x"}`), []byte(`{"id":"x"}`)); ok {
		t.Error("field strategy with no path produced a key")
	}
	if _, ok := dedupeKey(source(true, dedupeField, "$.id"), []byte("not json"), nil); ok {
		t.Error("field strategy with an unparsed body produced a key")
	}
}

func TestDedupeKeyUnknownStrategyFailsOpen(t *testing.T) {
	body := []byte(`{"id":"evt_1"}`)
	if _, ok := dedupeKey(source(true, "sha512-of-the-moon", ""), body, body); ok {
		t.Error("an unrecognized strategy produced a key")
	}
}

// The key is half of a btree primary key, so an attacker-controlled field can't
// be allowed to grow without bound.
func TestDedupeKeyLongFieldValueIsHashed(t *testing.T) {
	long := strings.Repeat("a", maxDedupeKeyLen+1)
	body := []byte(`{"id":"` + long + `"}`)

	key, ok := dedupeKey(source(true, dedupeField, "$.id"), body, body)
	if !ok {
		t.Fatal("no key produced")
	}
	if len(key) != sha256.Size*2 {
		t.Errorf("key length = %d, want a %d-char hash for an oversized value", len(key), sha256.Size*2)
	}

	// Still deterministic, and still distinct from a different long value.
	again, _ := dedupeKey(source(true, dedupeField, "$.id"), body, body)
	if again != key {
		t.Error("hashed key is not deterministic")
	}
	other := []byte(`{"id":"` + strings.Repeat("b", maxDedupeKeyLen+1) + `"}`)
	if k, _ := dedupeKey(source(true, dedupeField, "$.id"), other, other); k == key {
		t.Error("two different oversized values collided")
	}

	// A value exactly at the limit is kept readable.
	atLimit := strings.Repeat("c", maxDedupeKeyLen)
	body = []byte(`{"id":"` + atLimit + `"}`)
	if k, _ := dedupeKey(source(true, dedupeField, "$.id"), body, body); k != atLimit {
		t.Error("a value at the length limit was hashed instead of kept verbatim")
	}
}
