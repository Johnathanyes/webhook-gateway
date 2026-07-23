// Package auth guards admin endpoints. OSS v1 is single-user: a single
// shared admin password, checked in constant time.
package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// AdminOnly wraps h so it runs only when the request presents the admin
// password as a bearer token ("Authorization: Bearer <password>"). The
// comparison is constant-time so the password can't be recovered by timing
// responses.
func AdminOnly(password string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		token := strings.TrimPrefix(header, prefix)
		if !strings.HasPrefix(header, prefix) ||
			subtle.ConstantTimeCompare([]byte(token), []byte(password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="admin"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}
