package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webhook-gateway/internal/sourcedef"
)

func TestGenerateEndpointPath(t *testing.T) {
	const want = len("src_") + 32 // src_ + 16 bytes hex-encoded

	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		p, err := generateEndpointPath()
		if err != nil {
			t.Fatalf("generateEndpointPath: %v", err)
		}
		if !strings.HasPrefix(p, "src_") {
			t.Errorf("path %q missing src_ prefix", p)
		}
		if len(p) != want {
			t.Errorf("path %q length = %d, want %d", p, len(p), want)
		}
		if seen[p] {
			t.Errorf("duplicate path %q generated", p)
		}
		seen[p] = true
	}
}

// TestCreateSourceValidation covers the request-validation paths, which all
// reject before touching the database or encryptor — so a handler with only
// the catalog wired is enough to exercise them.
func TestCreateSourceValidation(t *testing.T) {
	catalog, err := sourcedef.Load()
	if err != nil {
		t.Fatalf("Load catalog: %v", err)
	}
	h := &sourcesHandler{catalog: catalog}

	tests := []struct {
		name string
		body string
	}{
		{"invalid json", `{`},
		{"missing name", `{"provider_type":"stripe","signing_secret":"x"}`},
		{"missing provider_type", `{"name":"s","signing_secret":"x"}`},
		{"unknown provider_type", `{"name":"s","provider_type":"nope","signing_secret":"x"}`},
		{"stripe without secret", `{"name":"s","provider_type":"stripe"}`},
		// Dedupe combinations that the table's CHECK constraints would reject.
		// Catching them here makes it a 400 instead of a 500 from Postgres.
		{"dedupe enabled without strategy", `{"name":"s","provider_type":"none","dedupe_enabled":true}`},
		{"dedupe unknown strategy", `{"name":"s","provider_type":"none","dedupe_enabled":true,"dedupe_strategy":"vibes"}`},
		{"dedupe field without path", `{"name":"s","provider_type":"none","dedupe_enabled":true,"dedupe_strategy":"field"}`},
		{"dedupe negative window", `{"name":"s","provider_type":"none","dedupe_enabled":true,"dedupe_strategy":"exact","dedupe_window_seconds":-1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/sources", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			h.create(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// Dedupe config is optional: a source that says nothing about it is not
// rejected, and the strategy fields are only required once it's switched on.
func TestCreateSourceDedupeIsOptional(t *testing.T) {
	for _, body := range []string{
		`{"name":"s","provider_type":"none"}`,
		`{"name":"s","provider_type":"none","dedupe_enabled":false,"dedupe_strategy":""}`,
	} {
		var req createSourceRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("decoding %s: %v", body, err)
		}
		if message, ok := validateDedupe(req); !ok {
			t.Errorf("validateDedupe(%s) rejected it: %s", body, message)
		}
	}
}
