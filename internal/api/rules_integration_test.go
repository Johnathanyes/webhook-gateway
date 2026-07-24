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
	"webhook-gateway/internal/tenancy"
)

// TestRulesAPIIntegration mirrors routes_integration_test.go: full CRUD against
// the compose Postgres, plus the rule-specific invariants the API owns
// (CEL compile check, action/destination validation). Skipped unless
// TEST_DATABASE_URL is set.
func TestRulesAPIIntegration(t *testing.T) {
	pool := testDB(t)
	q := db.New(pool)
	ctx := context.Background()

	src, err := q.InsertSource(ctx, db.InsertSourceParams{
		TenantID:           tenancy.DefaultTenantID,
		Name:               "rules-it-src",
		ProviderType:       "none",
		EndpointPath:       "src_rules_it_" + t.Name(),
		VerificationConfig: []byte("{}"),
	})
	if err != nil {
		t.Fatalf("inserting test source: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM sources WHERE id = $1", src.ID) })
	sourceID := uuidString(src.ID)

	dest, err := q.InsertDestination(ctx, db.InsertDestinationParams{
		TenantID:           tenancy.DefaultTenantID,
		Name:               "rules-it-dest",
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
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM destinations WHERE id = $1", dest.ID) })
	destID := uuidString(dest.ID)

	const adminPassword = "test-admin-password"
	mux := http.NewServeMux()
	RegisterRules(mux, q, middleware.NewAuth(q, adminPassword))

	authed := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+adminPassword)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// --- create a drop rule ---
	rec := authed(http.MethodPost, "/api/rules",
		`{"source_id":"`+sourceID+`","name":"drop-pings","expression":"body.kind == \"ping\"","action":"drop"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var created ruleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM rules WHERE id = $1", created.ID) })
	if created.Priority != 100 || !created.Enabled {
		t.Errorf("defaults not applied: priority=%d enabled=%v, want 100/true", created.Priority, created.Enabled)
	}

	// --- a non-compiling expression is a 400, not a stored rule ---
	if rec := authed(http.MethodPost, "/api/rules",
		`{"source_id":"`+sourceID+`","name":"bad","expression":"body.amount >","action":"drop"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad expression status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	// --- action=route without destinations is a 400 ---
	if rec := authed(http.MethodPost, "/api/rules",
		`{"source_id":"`+sourceID+`","name":"r","expression":"true","action":"route"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("route-without-dests status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	// --- unknown action is a 400 ---
	if rec := authed(http.MethodPost, "/api/rules",
		`{"source_id":"`+sourceID+`","name":"r","expression":"true","action":"transform"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown action status = %d, want 400", rec.Code)
	}

	// --- unknown source is a 400 (foreign key) ---
	if rec := authed(http.MethodPost, "/api/rules",
		`{"source_id":"00000000-0000-7000-8000-000000000099","name":"r","expression":"true","action":"drop"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown source status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	// --- get ---
	if rec := authed(http.MethodGet, "/api/rules/"+created.ID, ""); rec.Code != http.StatusOK {
		t.Errorf("get status = %d, want 200", rec.Code)
	}

	// --- list includes the created rule ---
	rec = authed(http.MethodGet, "/api/rules", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", rec.Code)
	}
	var listed []ruleResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &listed)
	var found bool
	for _, r := range listed {
		if r.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Error("created rule not in list")
	}

	// --- update to a route rule with a destination ---
	rec = authed(http.MethodPatch, "/api/rules/"+created.ID,
		`{"name":"route-big","expression":"body.amount > 100","action":"route","route_destination_ids":["`+destID+`"],"priority":50,"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var updated ruleResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Action != "route" || len(updated.RouteDestinationIDs) != 1 || updated.RouteDestinationIDs[0] != destID {
		t.Errorf("update route dests = %+v, want [%s]", updated.RouteDestinationIDs, destID)
	}
	if updated.Priority != 50 || updated.Enabled {
		t.Errorf("update priority/enabled = %d/%v, want 50/false", updated.Priority, updated.Enabled)
	}

	// --- delete, then delete again is 404 ---
	if rec := authed(http.MethodDelete, "/api/rules/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", rec.Code)
	}
	if rec := authed(http.MethodDelete, "/api/rules/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete again status = %d, want 404", rec.Code)
	}
}
