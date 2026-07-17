package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCreateDestinationValidation covers the request-validation paths, which
// all reject before touching the database — so a handler with no q wired is
// enough to exercise them.
func TestCreateDestinationValidation(t *testing.T) {
	h := &destinationsHandler{}

	tests := []struct {
		name string
		body string
	}{
		{"invalid json", `{`},
		{"missing name", `{"url":"https://example.com/hook"}`},
		{"missing url", `{"name":"d"}`},
		{"url missing scheme", `{"name":"d","url":"example.com/hook"}`},
		{"url wrong scheme", `{"name":"d","url":"ftp://example.com/hook"}`},
		{"non-positive rate limit", `{"name":"d","url":"https://example.com/hook","rate_limit_per_second":0}`},
		{"negative rate limit", `{"name":"d","url":"https://example.com/hook","rate_limit_per_second":-1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/destinations", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			h.create(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestCreateDestinationDefaults checks that omitted numeric fields are
// backfilled with the same defaults migration 00002 sets on the columns.
func TestCreateDestinationDefaults(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/destinations", strings.NewReader(
		`{"name":"d","url":"https://example.com/hook"}`))
	got, errMsg, ok := decodeDestinationRequest(req)
	if !ok {
		t.Fatalf("decodeDestinationRequest rejected valid body: %s", errMsg)
	}
	if got.TimeoutMs != defaultTimeoutMs {
		t.Errorf("TimeoutMs = %d, want %d", got.TimeoutMs, defaultTimeoutMs)
	}
	if got.MaxAttempts != defaultMaxAttempts {
		t.Errorf("MaxAttempts = %d, want %d", got.MaxAttempts, defaultMaxAttempts)
	}
	if got.BackoffBaseSeconds != defaultBackoffBaseSeconds {
		t.Errorf("BackoffBaseSeconds = %d, want %d", got.BackoffBaseSeconds, defaultBackoffBaseSeconds)
	}
	if got.BackoffMaxSeconds != defaultBackoffMaxSeconds {
		t.Errorf("BackoffMaxSeconds = %d, want %d", got.BackoffMaxSeconds, defaultBackoffMaxSeconds)
	}
	if got.RateLimitPerSecond != nil {
		t.Errorf("RateLimitPerSecond = %v, want nil (unlimited)", got.RateLimitPerSecond)
	}
}
