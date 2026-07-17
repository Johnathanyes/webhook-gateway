package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCreateRouteValidation covers the request-validation paths, which all
// reject before touching the database — so a handler with no q wired is
// enough to exercise them.
func TestCreateRouteValidation(t *testing.T) {
	h := &routesHandler{}

	tests := []struct {
		name string
		body string
	}{
		{"invalid json", `{`},
		{"missing source_id", `{"destination_id":"00000000-0000-7000-8000-000000000001"}`},
		{"invalid source_id", `{"source_id":"not-a-uuid","destination_id":"00000000-0000-7000-8000-000000000001"}`},
		{"invalid destination_id", `{"source_id":"00000000-0000-7000-8000-000000000001","destination_id":"not-a-uuid"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/routes", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			h.create(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
