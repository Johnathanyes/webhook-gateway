package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"webhook-gateway/internal/auth"
)

// The dashboard sends a cookie and no Authorization header. Like the admin
// password it stands in for, a session satisfies every scope.
func TestRequireScopeAcceptsSessionCookie(t *testing.T) {
	sessions := auth.NewSessions("ZGV2LTMyLWJ5dGUtZW5jcnlwdGlvbi1rZXktMDAwMDA=", testAdminPassword, nil)
	a := NewAuth(nil, testAdminPassword)
	a.AcceptSessions(sessions)

	// The cookie a login would have set.
	login := httptest.NewRecorder()
	sessions.Issue(login, httptest.NewRequest(http.MethodGet, "/", nil))
	cookie := login.Result().Cookies()[0]

	for _, scope := range Scopes {
		t.Run(scope, func(t *testing.T) {
			var reached bool
			req := httptest.NewRequest(http.MethodGet, "/api/anything", nil)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()

			a.RequireScope(scope, reachedHandler(&reached)).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if !reached {
				t.Error("handler did not run")
			}
		})
	}
}

// A garbage cookie must not be treated as a credential — and must fall through
// to the bearer path's 401 rather than panicking on the nil *db.Queries.
func TestRequireScopeRejectsForgedSessionCookie(t *testing.T) {
	sessions := auth.NewSessions("ZGV2LTMyLWJ5dGUtZW5jcnlwdGlvbi1rZXktMDAwMDA=", testAdminPassword, nil)
	a := NewAuth(nil, testAdminPassword)
	a.AcceptSessions(sessions)

	var reached bool
	req := httptest.NewRequest(http.MethodGet, "/api/anything", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "v1.99999999999.forged"})
	rec := httptest.NewRecorder()

	a.RequireScope(ScopeRead, reachedHandler(&reached)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if reached {
		t.Error("handler ran for a forged cookie")
	}
}
