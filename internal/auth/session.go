package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const SessionCookieName = "whg_session"

const (
	sessionTTL = 7 * 24 * time.Hour
	// Token layout: "v1.<unix expiry>.<hmac>". The version prefix is signed
	// along with the expiry, so a future format change can't be downgraded.
	sessionVersion = "v1"
)

// Sessions issues and verifies the dashboard login cookie. The cookie is
// stateless.
type Sessions struct {
	key []byte
	// nil means "decide per request from the connection"; a non-nil value is
	// the operator overriding that decision.
	secure *bool
	// Swapped in tests to drive expiry and renewal.
	now func() time.Time
}

// NewSessions derives the signing key from the two secrets the gateway already
// requires.
func NewSessions(encryptionKey, adminPassword string, secure *bool) *Sessions {
	mac := hmac.New(sha256.New, []byte(encryptionKey))
	mac.Write([]byte(adminPassword))
	return &Sessions{key: mac.Sum(nil), secure: secure, now: time.Now}
}

// Issue sets a fresh session cookie on w.
func (s *Sessions) Issue(w http.ResponseWriter, r *http.Request) {
	expiry := s.now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:  SessionCookieName,
		Value: s.token(expiry),
		Path:  "/",
		SameSite: http.SameSiteLaxMode,
		HttpOnly: true,
		Secure:   s.secureFor(r),
		Expires:  expiry,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

// Authenticate reports whether r carries a valid session, renewing the cookie
// on w once it is past half its life so an active admin is never logged out
// mid-task. w may be nil when the caller only wants the yes/no answer.
func (s *Sessions) Authenticate(w http.ResponseWriter, r *http.Request) bool {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return false
	}
	expiry, ok := s.verify(cookie.Value)
	if !ok {
		return false
	}
	if w != nil && expiry.Sub(s.now()) < sessionTTL/2 {
		s.Issue(w, r)
	}
	return true
}

// Clear expires the cookie in the browser. There is no server-side state to
// delete — that is the trade the stateless design makes.
func (s *Sessions) Clear(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
		HttpOnly: true,
		Secure:   s.secureFor(r),
		MaxAge:   -1,
	})
}

func (s *Sessions) token(expiry time.Time) string {
	payload := sessionVersion + "." + strconv.FormatInt(expiry.Unix(), 10)
	return payload + "." + s.sign(payload)
}

// verify returns the token's expiry and whether it is both authentic and
// unexpired.
func (s *Sessions) verify(token string) (time.Time, bool) {
	i := strings.LastIndex(token, ".")
	if i < 0 {
		return time.Time{}, false
	}
	payload, sig := token[:i], token[i+1:]
	if !hmac.Equal([]byte(sig), []byte(s.sign(payload))) {
		return time.Time{}, false
	}
	// Authentic, so the layout is ours: version prefix then expiry.
	version, unix, found := strings.Cut(payload, ".")
	if !found || version != sessionVersion {
		return time.Time{}, false
	}
	secs, err := strconv.ParseInt(unix, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	expiry := time.Unix(secs, 0)
	if !s.now().Before(expiry) {
		return time.Time{}, false
	}
	return expiry, true
}

func (s *Sessions) sign(payload string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// secureFor decides the Secure flag.
func (s *Sessions) secureFor(r *http.Request) bool {
	if s.secure != nil {
		return *s.secure
	}
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}
