// Package auth guards admin endpoints. OSS v1 is single-user: a single
// shared admin password, checked in constant time.
package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func AdminOnly(password string, sessions *Sessions, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sessions != nil && sessions.Authenticate(w, r) {
			h.ServeHTTP(w, r)
			return
		}

		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		token := strings.TrimPrefix(header, prefix)
		
		if !strings.HasPrefix(header, prefix) || password == "" ||
			subtle.ConstantTimeCompare([]byte(token), []byte(password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="admin"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}
