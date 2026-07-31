package api

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"webhook-gateway/internal/auth"
)


func RegisterAuth(mux *http.ServeMux, sessions *auth.Sessions, adminPassword string) {
	h := &authHandler{sessions: sessions, adminPassword: adminPassword}
	mux.Handle("POST /api/auth/login", http.HandlerFunc(h.login))
	mux.Handle("POST /api/auth/logout", http.HandlerFunc(h.logout))
	mux.Handle("GET /api/auth/session", http.HandlerFunc(h.session))
}

type authHandler struct {
	sessions      *auth.Sessions
	adminPassword string
}

func (h *authHandler) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Constant-time, same as every other admin password check.
	if h.adminPassword == "" ||
		subtle.ConstantTimeCompare([]byte(req.Password), []byte(h.adminPassword)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid password")
		return
	}

	h.sessions.Issue(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (h *authHandler) logout(w http.ResponseWriter, r *http.Request) {
	h.sessions.Clear(w, r)
	writeJSON(w, http.StatusNoContent, nil)
}

func (h *authHandler) session(w http.ResponseWriter, r *http.Request) {
	if !h.sessions.Authenticate(w, r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}
