package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webhook-gateway/internal/api/middleware"
	"webhook-gateway/internal/auth"
	"webhook-gateway/internal/db"
)

// TestAPIKeysIntegration covers the #23 done-criteria against the compose
// Postgres: a `read`-scoped key can list sources but gets 403 on create, a
// revoked key gets 401, and key management itself stays admin-password-only.
// Skipped unless TEST_DATABASE_URL is set — see sources_integration_test.go.
func TestAPIKeysIntegration(t *testing.T) {
	pool := testDB(t)
	q := db.New(pool)

	const adminPassword = "test-admin-password"
	mux := http.NewServeMux()
	RegisterAPIKeys(mux, q, adminPassword)
	// A real scoped surface to exercise the middleware against.
	RegisterSources(mux, q, testEncryptor(t), testCatalog(t), middleware.NewAuth(q, adminPassword))

	do := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// --- create a read-only key (admin credential) ---
	name := "apikeys-it-" + randHex(t)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM api_keys WHERE name = $1", name)
	})
	rec := do(http.MethodPost, "/api/api-keys", adminPassword, `{"name":"`+name+`","scopes":["read"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create key: got %d, want 201: %s", rec.Code, rec.Body)
	}
	var created struct {
		ID        string `json:"id"`
		Key       string `json:"key"`
		KeyPrefix string `json:"key_prefix"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	if !strings.HasPrefix(created.Key, "whg_") {
		t.Errorf("plaintext key %q does not start with whg_", created.Key)
	}
	if created.KeyPrefix != created.Key[:auth.KeyPrefixLen] {
		t.Errorf("key_prefix %q is not the first %d chars of the key", created.KeyPrefix, auth.KeyPrefixLen)
	}

	// --- the plaintext appears at creation and never again ---
	rec = do(http.MethodGet, "/api/api-keys", adminPassword, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list keys: got %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), created.Key) {
		t.Error("list response re-shows the plaintext key")
	}

	// --- read scope: sources list 200, sources create 403 ---
	if rec = do(http.MethodGet, "/api/sources", created.Key, ""); rec.Code != http.StatusOK {
		t.Errorf("GET /api/sources with read key: got %d, want 200: %s", rec.Code, rec.Body)
	}
	rec = do(http.MethodPost, "/api/sources", created.Key, `{"name":"x","provider_type":"none"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /api/sources with read key: got %d, want 403: %s", rec.Code, rec.Body)
	}

	// --- key management is admin-only: a valid key can't touch it ---
	if rec = do(http.MethodGet, "/api/api-keys", created.Key, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/api-keys with API key: got %d, want 401", rec.Code)
	}

	// --- unknown credentials, both shapes ---
	if rec = do(http.MethodGet, "/api/sources", "whg_"+randHex(t), ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/sources with made-up key: got %d, want 401", rec.Code)
	}
	if rec = do(http.MethodGet, "/api/sources", "wrong-password", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/sources with wrong admin password: got %d, want 401", rec.Code)
	}

	// --- validation ---
	rec = do(http.MethodPost, "/api/api-keys", adminPassword, `{"name":"x","scopes":["admin"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("create with unknown scope: got %d, want 400", rec.Code)
	}
	rec = do(http.MethodPost, "/api/api-keys", adminPassword, `{"name":"","scopes":["read"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("create with empty name: got %d, want 400", rec.Code)
	}

	// --- revoke: 204, then the key is dead (401), then 404 on repeat ---
	if rec = do(http.MethodDelete, "/api/api-keys/"+created.ID, adminPassword, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke key: got %d, want 204: %s", rec.Code, rec.Body)
	}
	if rec = do(http.MethodGet, "/api/sources", created.Key, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/sources with revoked key: got %d, want 401", rec.Code)
	}
	if rec = do(http.MethodDelete, "/api/api-keys/"+created.ID, adminPassword, ""); rec.Code != http.StatusNotFound {
		t.Errorf("second revoke: got %d, want 404", rec.Code)
	}
}
