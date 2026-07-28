package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// These cover the credentials RequireScope resolves without ever consulting the
// database: the admin password, and every way a request can fail to present a
// usable bearer token. The API-key paths need Postgres and are covered by
// TestAPIKeysIntegration in internal/api (read scope 200, missing scope 403,
// revoked key 401, unknown key 401).
//
// NewAuth is deliberately given a nil *db.Queries here: reaching it would mean
// the middleware went to the database for a credential that isn't key-shaped,
// so a nil dereference is the failure signal we want.

const testAdminPassword = "test-admin-password"

// reachedHandler reports whether the wrapped handler ran, which is the real
// question for an auth middleware — a 200 with no handler would pass a naive
// status-code assertion.
func reachedHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

func request(t *testing.T, scope, authHeader string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	return requestAs(t, testAdminPassword, scope, authHeader)
}

func requestAs(t *testing.T, adminPassword, scope, authHeader string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	var reached bool
	a := NewAuth(nil, adminPassword)
	req := httptest.NewRequest(http.MethodGet, "/api/anything", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	a.RequireScope(scope, reachedHandler(&reached)).ServeHTTP(rec, req)
	return rec, reached
}

// The admin password is the root credential: it satisfies every scope, which is
// what lets key management stay admin-only while the rest of the API accepts
// scoped keys.
func TestRequireScopeAcceptsAdminPasswordForEveryScope(t *testing.T) {
	for _, scope := range Scopes {
		t.Run(scope, func(t *testing.T) {
			rec, reached := request(t, scope, "Bearer "+testAdminPassword)
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if !reached {
				t.Error("handler did not run")
			}
		})
	}
}

func TestRequireScopeRejectsWrongPassword(t *testing.T) {
	rec, reached := request(t, ScopeRead, "Bearer not-the-password")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if reached {
		t.Error("handler ran for a bad credential")
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="api"` {
		t.Errorf("WWW-Authenticate = %q, want `Bearer realm=\"api\"`", got)
	}
	// Every rejection uses the same {"error": ...} envelope the API uses, so a
	// client can parse failures uniformly.
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding error body %q: %v", rec.Body.String(), err)
	}
	if body["error"] != "unauthorized" {
		t.Errorf("error = %q, want %q", body["error"], "unauthorized")
	}
}

// The comparison is exact, not a prefix match — a truncated or extended
// password must not authenticate.
func TestRequireScopeMatchesAdminPasswordExactly(t *testing.T) {
	for _, token := range []string{
		testAdminPassword + "-extra",
		testAdminPassword[:len(testAdminPassword)-1],
		"",
	} {
		t.Run(token, func(t *testing.T) {
			rec, reached := request(t, ScopeRead, "Bearer "+token)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if reached {
				t.Error("handler ran for a non-matching password")
			}
		})
	}
}

// An unconfigured admin password authenticates nobody. Without the non-empty
// guard, comparing two empty strings succeeds and a bare "Bearer " header would
// satisfy every scope. Config validation makes this unreachable in the shipped
// binary; the guard is what keeps it unreachable for a future caller that
// constructs Auth directly — session auth or the auth interface.
func TestRequireScopeRejectsEverythingWhenAdminPasswordIsEmpty(t *testing.T) {
	for _, header := range []string{"Bearer ", "Bearer", "Bearer anything", ""} {
		t.Run("header="+header, func(t *testing.T) {
			rec, reached := requestAs(t, "", ScopeRead, header)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if reached {
				t.Error("handler ran with no admin password configured")
			}
		})
	}
}

// Only the exact "Bearer " scheme is accepted; anything else is unauthenticated
// rather than being coerced into a token.
func TestRequireScopeRequiresBearerPrefix(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"basic auth", "Basic dGVzdC1hZG1pbi1wYXNzd29yZA=="},
		{"scheme without space", "Bearer"},
		{"lowercase scheme", "bearer " + testAdminPassword},
		{"other scheme", "Token " + testAdminPassword},
		{"bare password", testAdminPassword},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, reached := request(t, ScopeRead, tt.header)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if reached {
				t.Error("handler ran without a bearer credential")
			}
		})
	}
}
