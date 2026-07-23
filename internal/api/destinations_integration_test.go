package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webhook-gateway/internal/api/middleware"
	"webhook-gateway/internal/db"
)

// TestDestinationsAPIIntegration drives the destinations API against the
// compose Postgres. Skipped unless TEST_DATABASE_URL is set — see
// sources_integration_test.go for how to run it.
func TestDestinationsAPIIntegration(t *testing.T) {
	pool := testDB(t)
	q := db.New(pool)

	const adminPassword = "test-admin-password"
	mux := http.NewServeMux()
	RegisterDestinations(mux, q, middleware.NewAuth(q, adminPassword))

	authed := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+adminPassword)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// --- create ---
	createRec := authed(http.MethodPost, "/api/destinations",
		`{"name":"integration-test-dest","url":"https://example.com/hook","max_attempts":5}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", createRec.Code, createRec.Body.String())
	}
	var created destinationResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM destinations WHERE id = $1", created.ID)
	})
	if created.MaxAttempts != 5 {
		t.Errorf("MaxAttempts = %d, want 5", created.MaxAttempts)
	}
	if created.TimeoutMs != defaultTimeoutMs {
		t.Errorf("TimeoutMs = %d, want default %d", created.TimeoutMs, defaultTimeoutMs)
	}
	if created.Paused {
		t.Error("newly created destination is paused")
	}

	// --- get ---
	getRec := authed(http.MethodGet, "/api/destinations/"+created.ID, "")
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200; body=%s", getRec.Code, getRec.Body.String())
	}

	// --- get unknown id ---
	if rec := authed(http.MethodGet, "/api/destinations/00000000-0000-7000-8000-000000000099", ""); rec.Code != http.StatusNotFound {
		t.Errorf("get unknown status = %d, want 404", rec.Code)
	}

	// --- list includes it ---
	listRec := authed(http.MethodGet, "/api/destinations", "")
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body=%s", listRec.Code, listRec.Body.String())
	}
	var listed []destinationResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decoding list response: %v", err)
	}
	found := false
	for _, d := range listed {
		if d.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("created destination %q not present in list", created.ID)
	}

	// --- update ---
	updateRec := authed(http.MethodPatch, "/api/destinations/"+created.ID,
		`{"name":"renamed","url":"https://example.com/hook2","max_attempts":3}`)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200; body=%s", updateRec.Code, updateRec.Body.String())
	}
	var updated destinationResponse
	if err := json.Unmarshal(updateRec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decoding update response: %v", err)
	}
	if updated.Name != "renamed" || updated.MaxAttempts != 3 {
		t.Errorf("update did not apply: %+v", updated)
	}

	// --- pause / resume ---
	pauseRec := authed(http.MethodPost, "/api/destinations/"+created.ID+"/pause", "")
	if pauseRec.Code != http.StatusOK {
		t.Fatalf("pause status = %d, want 200; body=%s", pauseRec.Code, pauseRec.Body.String())
	}
	var paused destinationResponse
	_ = json.Unmarshal(pauseRec.Body.Bytes(), &paused)
	if !paused.Paused {
		t.Error("destination not marked paused after pause")
	}

	resumeRec := authed(http.MethodPost, "/api/destinations/"+created.ID+"/resume", "")
	if resumeRec.Code != http.StatusOK {
		t.Fatalf("resume status = %d, want 200; body=%s", resumeRec.Code, resumeRec.Body.String())
	}
	var resumed destinationResponse
	_ = json.Unmarshal(resumeRec.Body.Bytes(), &resumed)
	if resumed.Paused {
		t.Error("destination still marked paused after resume")
	}

	// --- unauthenticated request rejected ---
	noAuthRec := httptest.NewRecorder()
	mux.ServeHTTP(noAuthRec, httptest.NewRequest(http.MethodGet, "/api/destinations", nil))
	if noAuthRec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated list status = %d, want 401", noAuthRec.Code)
	}

	// --- delete ---
	deleteRec := authed(http.MethodDelete, "/api/destinations/"+created.ID, "")
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204; body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if rec := authed(http.MethodGet, "/api/destinations/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete status = %d, want 404", rec.Code)
	}
	if rec := authed(http.MethodDelete, "/api/destinations/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete again status = %d, want 404", rec.Code)
	}
}
