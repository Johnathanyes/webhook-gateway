package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"webhook-gateway/internal/api/middleware"
	"webhook-gateway/internal/auth"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/tenancy"
)

// Mounts API-key management, guarded by the admin password
// only.
func RegisterAPIKeys(mux *http.ServeMux, q *db.Queries, adminPassword string) {
	h := &apiKeysHandler{q: q}
	mux.Handle("POST /api/api-keys", auth.AdminOnly(adminPassword, http.HandlerFunc(h.create)))
	mux.Handle("GET /api/api-keys", auth.AdminOnly(adminPassword, http.HandlerFunc(h.list)))
	mux.Handle("DELETE /api/api-keys/{id}", auth.AdminOnly(adminPassword, http.HandlerFunc(h.revoke)))
}

type apiKeysHandler struct {
	q *db.Queries
}

type createAPIKeyRequest struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

// apiKeyResponse never includes the key hash; key_prefix is the only
// key-derived field a caller ever sees again after creation.
type apiKeyResponse struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// createAPIKeyResponse carries the plaintext key exactly once, at creation.
type createAPIKeyResponse struct {
	apiKeyResponse
	Key string `json:"key"`
}

func (h *apiKeysHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.Scopes) == 0 {
		writeError(w, http.StatusBadRequest, "at least one scope is required")
		return
	}
	for _, s := range req.Scopes {
		if !slices.Contains(middleware.Scopes, s) {
			writeError(w, http.StatusBadRequest, "unknown scope "+s)
			return
		}
	}

	plaintext, hash, prefix, err := auth.GenerateAPIKey()
	if err != nil {
		slog.Error("generating API key", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	key, err := h.q.InsertApiKey(r.Context(), db.InsertApiKeyParams{
		TenantID:  tenancy.DefaultTenantID,
		Name:      req.Name,
		KeyPrefix: prefix,
		KeyHash:   hash,
		Scopes:    req.Scopes,
	})
	if err != nil {
		slog.Error("inserting API key", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, createAPIKeyResponse{
		apiKeyResponse: toAPIKeyResponse(key),
		Key:            plaintext,
	})
}

func (h *apiKeysHandler) list(w http.ResponseWriter, r *http.Request) {
	keys, err := h.q.ListAPIKeys(r.Context(), tenancy.DefaultTenantID)
	if err != nil {
		slog.Error("listing API keys", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]apiKeyResponse, len(keys))
	for i, k := range keys {
		out[i] = toAPIKeyResponse(k)
	}
	writeJSON(w, http.StatusOK, out)
}

// revoke soft-deletes: the row survives with revoked_at set so the list view
// keeps an audit trail of what existed, and the UNIQUE key_prefix can never
// be silently reused.
func (h *apiKeysHandler) revoke(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid key id")
		return
	}
	rows, err := h.q.RevokeAPIKey(r.Context(), db.RevokeAPIKeyParams{
		ID:       id,
		TenantID: tenancy.DefaultTenantID,
	})
	if err != nil {
		slog.Error("revoking API key", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if rows == 0 {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func toAPIKeyResponse(k db.ApiKey) apiKeyResponse {
	return apiKeyResponse{
		ID:         uuidString(k.ID),
		Name:       k.Name,
		KeyPrefix:  k.KeyPrefix,
		Scopes:     k.Scopes,
		CreatedAt:  k.CreatedAt.Time,
		LastUsedAt: timePtr(k.LastUsedAt),
		RevokedAt:  timePtr(k.RevokedAt),
	}
}

// timePtr renders a nullable timestamp as nil (omitted from JSON) when unset.
func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}
