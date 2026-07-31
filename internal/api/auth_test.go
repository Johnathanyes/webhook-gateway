package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webhook-gateway/internal/auth"
)

const (
	testSessionKey      = "ZGV2LTMyLWJ5dGUtZW5jcnlwdGlvbi1rZXktMDAwMDA="
	testSessionPassword = "test-admin-password"
)

func authMux(t *testing.T) (*http.ServeMux, *auth.Sessions) {
	t.Helper()
	sessions := auth.NewSessions(testSessionKey, testSessionPassword, nil)
	mux := http.NewServeMux()
	RegisterAuth(mux, sessions, testSessionPassword)
	return mux, sessions
}

func post(t *testing.T, mux *http.ServeMux, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in response", auth.SessionCookieName)
	return nil
}

func TestLoginSetsSessionCookie(t *testing.T) {
	mux, _ := authMux(t)

	rec := post(t, mux, "/api/auth/login", `{"password":"`+testSessionPassword+`"}`, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	cookie := sessionCookie(t, rec)
	if !cookie.HttpOnly {
		t.Error("session cookie is not HttpOnly")
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	mux, _ := authMux(t)

	rec := post(t, mux, "/api/auth/login", `{"password":"wrong"}`, nil)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a failed login set a cookie")
	}
}

// The SPA calls this on load to choose between the app and the login page.
func TestSessionEndpointReflectsLoginState(t *testing.T) {
	mux, _ := authMux(t)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", rec.Code)
	}

	cookie := sessionCookie(t, post(t, mux, "/api/auth/login", `{"password":"`+testSessionPassword+`"}`, nil))

	req = httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("logged-in status = %d, want 200", rec.Code)
	}
}

func TestLogoutExpiresTheCookie(t *testing.T) {
	mux, _ := authMux(t)
	cookie := sessionCookie(t, post(t, mux, "/api/auth/login", `{"password":"`+testSessionPassword+`"}`, nil))

	rec := post(t, mux, "/api/auth/logout", "", cookie)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if cleared := sessionCookie(t, rec); cleared.MaxAge >= 0 {
		t.Errorf("logout MaxAge = %d, want negative", cleared.MaxAge)
	}
}

func TestLoginRejectsMalformedBody(t *testing.T) {
	mux, _ := authMux(t)

	rec := post(t, mux, "/api/auth/login", `not json`, nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
