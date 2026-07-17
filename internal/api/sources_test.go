package api

import (
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
