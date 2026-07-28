package ingest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"webhook-gateway/internal/db"
)

// Dedupe strategies, matching the chk_dedupe_strategy constraint on sources.
const (
	dedupeExact = "exact"
	dedupeField = "field"
)

// droppedReasonDuplicate is written to events.dropped_reason when an event
// loses the race for its dedupe key, alongside the rule names written there by
// the filtering path.
const droppedReasonDuplicate = "duplicate"

const defaultDedupeWindowSeconds = 300
const maxDedupeKeyLen = 256

// dedupeKey derives the dedupe key for an event per the source's configured
// strategy. ok is false when dedupe is off for this source, or when the
// key cannot be derived — an unparseable body, a missing field, or a field
// holding an object rather than a scalar. That case fails open: the event is
// delivered normally rather than being silently dropped or lumped in with
// unrelated events under an empty key.
func dedupeKey(source db.Source, body, parsedBody []byte) (string, bool) {
	if !source.DedupeEnabled {
		return "", false
	}

	switch source.DedupeStrategy.String {
	case dedupeExact:
		// The verbatim bytes, so two payloads dedupe only if they are truly
		// identical — whitespace and key order included.
		sum := sha256.Sum256(body)
		return hex.EncodeToString(sum[:]), true

	case dedupeField:
		if parsedBody == nil || !source.DedupeFieldPath.Valid {
			return "", false
		}
		value, ok := jsonFieldValue(parsedBody, source.DedupeFieldPath.String)
		if !ok {
			return "", false
		}
		return shorten(value), true

	default:
		return "", false
	}
}

// jsonFieldValue reads a scalar out of a JSON document at a dotted path such as
// "$.id" or "$.data.object.id". Object traversal only: array indexing is not
// supported, because no provider's idempotency key lives behind one.
func jsonFieldValue(document []byte, path string) (string, bool) {
	segments := strings.Split(strings.TrimPrefix(strings.TrimPrefix(path, "$"), "."), ".")
	if len(segments) == 0 || segments[0] == "" {
		return "", false
	}

	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var current any
	if err := decoder.Decode(&current); err != nil {
		return "", false
	}

	for _, segment := range segments {
		object, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		current, ok = object[segment]
		if !ok {
			return "", false
		}
	}

	switch v := current.(type) {
	case string:
		// An empty string is not a usable identity.
		if v == "" {
			return "", false
		}
		return v, true
	case json.Number:
		return v.String(), true
	case bool:
		// Legal but never an identity; treating it as one would collapse every
		// event into two buckets.
		return "", false
	default:
		// null, objects, arrays.
		return "", false
	}
}

func shorten(value string) string {
	if len(value) <= maxDedupeKeyLen {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
