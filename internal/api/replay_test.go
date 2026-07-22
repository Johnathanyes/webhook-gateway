package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestReplayValidation covers the request-validation paths of the replay API,
// which all reject before touching the database or River — so a handler with no
// pool/q/river wired is enough to exercise them.
func TestReplayValidation(t *testing.T) {
	h := &replayHandler{}

	t.Run("single replay rejects a bad event id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/events/not-a-uuid/replay", nil)
		req.SetPathValue("id", "not-a-uuid")
		rec := httptest.NewRecorder()
		h.replayEvent(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})

	bulkCases := []struct {
		name string
		body string
	}{
		{"invalid json", `{`},
		{"invalid source_id", `{"source_id":"not-a-uuid"}`},
		{"invalid delivery_status", `{"delivery_status":"bogus"}`},
	}
	for _, tc := range bulkCases {
		t.Run("bulk replay rejects "+tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/replays", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			h.bulkReplay(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
