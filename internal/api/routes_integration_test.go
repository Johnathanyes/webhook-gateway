package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webhook-gateway/internal/db"
	"webhook-gateway/internal/tenancy"
)

// TestRoutesAPIIntegration drives the routes API against the compose
// Postgres. Skipped unless TEST_DATABASE_URL is set — see
// sources_integration_test.go for how to run it.
func TestRoutesAPIIntegration(t *testing.T) {
	pool := testDB(t)
	q := db.New(pool)

	src, err := q.InsertSource(context.Background(), db.InsertSourceParams{
		TenantID:           tenancy.DefaultTenantID,
		Name:               "routes-it-src",
		ProviderType:       "none",
		EndpointPath:       "src_routes_it_" + t.Name(),
		VerificationConfig: []byte("{}"),
	})
	if err != nil {
		t.Fatalf("inserting test source: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM sources WHERE id = $1", src.ID) })

	dest, err := q.InsertDestination(context.Background(), db.InsertDestinationParams{
		TenantID:           tenancy.DefaultTenantID,
		Name:               "routes-it-dest",
		Url:                "https://example.com/hook",
		AuthConfig:         []byte("{}"),
		TimeoutMs:          defaultTimeoutMs,
		MaxAttempts:        defaultMaxAttempts,
		BackoffBaseSeconds: defaultBackoffBaseSeconds,
		BackoffMaxSeconds:  defaultBackoffMaxSeconds,
	})
	if err != nil {
		t.Fatalf("inserting test destination: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM destinations WHERE id = $1", dest.ID) })

	sourceID := uuidString(src.ID)
	destinationID := uuidString(dest.ID)

	const adminPassword = "test-admin-password"
	mux := http.NewServeMux()
	RegisterRoutes(mux, q, adminPassword)

	authed := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+adminPassword)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// --- create ---
	createRec := authed(http.MethodPost, "/api/routes",
		`{"source_id":"`+sourceID+`","destination_id":"`+destinationID+`"}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", createRec.Code, createRec.Body.String())
	}
	var created routeResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM routes WHERE id = $1", created.ID) })
	if !created.Enabled {
		t.Error("route not enabled by default")
	}

	// --- duplicate create is rejected (unique source/destination pair) ---
	dupRec := authed(http.MethodPost, "/api/routes",
		`{"source_id":"`+sourceID+`","destination_id":"`+destinationID+`"}`)
	if dupRec.Code != http.StatusConflict {
		t.Errorf("duplicate create status = %d, want 409; body=%s", dupRec.Code, dupRec.Body.String())
	}

	// --- create with unknown destination is rejected ---
	fkRec := authed(http.MethodPost, "/api/routes",
		`{"source_id":"`+sourceID+`","destination_id":"00000000-0000-7000-8000-000000000099"}`)
	if fkRec.Code != http.StatusBadRequest {
		t.Errorf("unknown destination create status = %d, want 400; body=%s", fkRec.Code, fkRec.Body.String())
	}

	// --- list filtered by source_id ---
	listRec := authed(http.MethodGet, "/api/routes?source_id="+sourceID, "")
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body=%s", listRec.Code, listRec.Body.String())
	}
	var listed []routeResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decoding list response: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Errorf("filtered list = %+v, want exactly the created route", listed)
	}

	// --- list filtered by unrelated destination_id returns empty ---
	emptyRec := authed(http.MethodGet, "/api/routes?destination_id=00000000-0000-7000-8000-000000000099", "")
	var empty []routeResponse
	_ = json.Unmarshal(emptyRec.Body.Bytes(), &empty)
	if len(empty) != 0 {
		t.Errorf("filtered list by unrelated destination = %+v, want empty", empty)
	}

	// --- update (disable) ---
	updateRec := authed(http.MethodPatch, "/api/routes/"+created.ID, `{"enabled":false}`)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200; body=%s", updateRec.Code, updateRec.Body.String())
	}
	var updated routeResponse
	_ = json.Unmarshal(updateRec.Body.Bytes(), &updated)
	if updated.Enabled {
		t.Error("route still enabled after disabling")
	}

	// --- delete ---
	deleteRec := authed(http.MethodDelete, "/api/routes/"+created.ID, "")
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204; body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if rec := authed(http.MethodDelete, "/api/routes/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete again status = %d, want 404", rec.Code)
	}
}
