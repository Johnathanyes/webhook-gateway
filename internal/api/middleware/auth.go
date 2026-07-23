// Package middleware aithenticates API requests
// Two credentials are accepted: the admin passowrd and api keys.

package middleware

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"webhook-gateway/internal/auth"
	"webhook-gateway/internal/db"
)

// The scope vocabulary: read = all
// GETs, write = config mutations, replay = re-delivery surfaces, tunnel =
// WebSocket endpoint.
const (
	ScopeRead   = "read"
	ScopeWrite  = "write"
	ScopeReplay = "replay"
	ScopeTunnel = "tunnel"
)

// Scopes is the set a new key may request, in display order.
var Scopes = []string{ScopeRead, ScopeWrite, ScopeReplay, ScopeTunnel}

// Auth resolves bearer tokens. One instance is shared by every handler
// registration so they all agree on the same credentials.
type Auth struct {
	q			  *db.Queries
	adminPassword string
}

func NewAuth(q *db.Queries, adminPassword string) *Auth {
	return &Auth{q: q, adminPassword: adminPassword}
}

// RequireScope wraps h so it runs only for the admin password or
// an active API key that carries the required scope. Unknown or revoked
// credentials get 401; a valid key missing the scope gets 403 — that
// distinction tells a caller whether to fix the key or the request.
func (a *Auth) RequireScope(scope string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) {
			unauthorized(w)
			return
		}
		token := strings.TrimPrefix(header, prefix)

		// Anything not key-shaped is compared against the admin password in
		// constant time, exactly like auth.AdminOnly always has.
		if !auth.LooksLikeAPIKey(token) {
			if subtle.ConstantTimeCompare([]byte(token), []byte(a.adminPassword)) == 1 {
				h.ServeHTTP(w, r)
				return
			}
			unauthorized(w)
			return
		}

		key, err := a.q.GetAPIKeyByHash(r.Context(), auth.HashAPIKey(token))
		if err != nil {
			// The query filters revoked_at IS NULL, so revoked and nonexistent
			// keys are the same 401. Real DB failures are not auth failures.
			if errors.Is(err, pgx.ErrNoRows) {
				unauthorized(w)
				return
			}
			slog.Error("looking up API key", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !slices.Contains(key.Scopes, scope) {
			http.Error(w, "missing scope: "+scope, http.StatusForbidden)
			return
		}

		// Best-effort: last_used_at is a UX nicety, so it must never fail or
		// slow down the request it's recording.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := a.q.TouchAPIKeyLastUsed(ctx, key.ID); err != nil {
				slog.Warn("touching api key last_used_at", "error", err)
			}
		}()

		h.ServeHTTP(w, r)
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}