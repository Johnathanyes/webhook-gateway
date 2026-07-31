package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const (
	testEncryptionKey = "ZGV2LTMyLWJ5dGUtZW5jcnlwdGlvbi1rZXktMDAwMDA="
	testPassword      = "hunter2"
)

// issued returns a request carrying the cookie s just set, i.e. the browser's
// next request.
func issued(t *testing.T, s *Sessions) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Issue(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Issue set %d cookies, want 1", len(cookies))
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(cookies[0])
	return r
}

func TestIssuedSessionAuthenticates(t *testing.T) {
	s := NewSessions(testEncryptionKey, testPassword, nil)

	if !s.Authenticate(nil, issued(t, s)) {
		t.Fatal("freshly issued session did not authenticate")
	}
}

func TestNoCookieDoesNotAuthenticate(t *testing.T) {
	s := NewSessions(testEncryptionKey, testPassword, nil)

	if s.Authenticate(nil, httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("request without a cookie authenticated")
	}
}

func TestTamperedTokenIsRejected(t *testing.T) {
	s := NewSessions(testEncryptionKey, testPassword, nil)

	// Push the expiry far into the future without re-signing — the forgery a
	// stateless cookie has to withstand.
	forged := "v1." + "99999999999" + "." + s.sign("v1.1")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: forged})

	if s.Authenticate(nil, r) {
		t.Fatal("tampered token authenticated")
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	s := NewSessions(testEncryptionKey, testPassword, nil)
	r := issued(t, s)

	s.now = func() time.Time { return time.Now().Add(sessionTTL + time.Minute) }

	if s.Authenticate(nil, r) {
		t.Fatal("expired session authenticated")
	}
}

// Rotating the admin password must log everyone out — the reason the signing
// key is derived from it.
func TestPasswordChangeInvalidatesSessions(t *testing.T) {
	old := NewSessions(testEncryptionKey, testPassword, nil)
	r := issued(t, old)

	rotated := NewSessions(testEncryptionKey, "new-password", nil)

	if rotated.Authenticate(nil, r) {
		t.Fatal("session survived an admin password change")
	}
}

func TestSessionRenewsPastHalfLife(t *testing.T) {
	s := NewSessions(testEncryptionKey, testPassword, nil)
	r := issued(t, s)

	// Just inside the first half: nothing to do.
	s.now = func() time.Time { return time.Now().Add(sessionTTL/2 - time.Hour) }
	rec := httptest.NewRecorder()
	if !s.Authenticate(rec, r) {
		t.Fatal("session did not authenticate")
	}
	if got := len(rec.Result().Cookies()); got != 0 {
		t.Errorf("renewed early: %d cookies set, want 0", got)
	}

	// Past half-life: the response should carry a fresh cookie.
	s.now = func() time.Time { return time.Now().Add(sessionTTL/2 + time.Hour) }
	rec = httptest.NewRecorder()
	if !s.Authenticate(rec, r) {
		t.Fatal("session did not authenticate past half-life")
	}
	if got := len(rec.Result().Cookies()); got != 1 {
		t.Fatalf("renewal set %d cookies, want 1", got)
	}
}

func TestClearExpiresTheCookie(t *testing.T) {
	s := NewSessions(testEncryptionKey, testPassword, nil)

	rec := httptest.NewRecorder()
	s.Clear(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Clear set %d cookies, want 1", len(cookies))
	}
	if cookies[0].MaxAge >= 0 {
		t.Errorf("MaxAge = %d, want negative", cookies[0].MaxAge)
	}
}

func TestCookieFlags(t *testing.T) {
	yes, no := true, false

	tests := []struct {
		name       string
		secure     *bool
		forwarded  string
		wantSecure bool
	}{
		{name: "plain http localhost", wantSecure: false},
		{name: "behind a TLS proxy", forwarded: "https", wantSecure: true},
		{name: "forced on", secure: &yes, wantSecure: true},
		{name: "forced off behind a TLS proxy", secure: &no, forwarded: "https", wantSecure: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSessions(testEncryptionKey, testPassword, tc.secure)
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}

			rec := httptest.NewRecorder()
			s.Issue(rec, r)
			cookie := rec.Result().Cookies()[0]

			if cookie.Secure != tc.wantSecure {
				t.Errorf("Secure = %v, want %v", cookie.Secure, tc.wantSecure)
			}
			if !cookie.HttpOnly {
				t.Error("HttpOnly = false, want true")
			}
			if cookie.SameSite != http.SameSiteLaxMode {
				t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
			}
		})
	}
}
