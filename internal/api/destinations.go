package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"webhook-gateway/internal/auth"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/tenancy"
)

const (
	defaultTimeoutMs = 10000
	// 16 attempts with the exponential backoff below spreads retries across
	// roughly three days
	defaultMaxAttempts        = 16
	defaultBackoffBaseSeconds = 30
	defaultBackoffMaxSeconds  = 43200
)

// RegisterDestinations mounts the destinations CRUD + pause/resume API on mux,
// guarded by the admin password.
func RegisterDestinations(mux *http.ServeMux, q *db.Queries, adminPassword string) {
	h := &destinationsHandler{q: q}
	mux.Handle("POST /api/destinations", auth.AdminOnly(adminPassword, http.HandlerFunc(h.create)))
	mux.Handle("GET /api/destinations", auth.AdminOnly(adminPassword, http.HandlerFunc(h.list)))
	mux.Handle("GET /api/destinations/{id}", auth.AdminOnly(adminPassword, http.HandlerFunc(h.get)))
	mux.Handle("PATCH /api/destinations/{id}", auth.AdminOnly(adminPassword, http.HandlerFunc(h.update)))
	mux.Handle("DELETE /api/destinations/{id}", auth.AdminOnly(adminPassword, http.HandlerFunc(h.delete)))
	mux.Handle("POST /api/destinations/{id}/pause", auth.AdminOnly(adminPassword, http.HandlerFunc(h.pause)))
	mux.Handle("POST /api/destinations/{id}/resume", auth.AdminOnly(adminPassword, http.HandlerFunc(h.resume)))
}

type destinationsHandler struct {
	q *db.Queries
}

type destinationRequest struct {
	Name               string          `json:"name"`
	URL                string          `json:"url"`
	AuthConfig         json.RawMessage `json:"auth_config"`
	TimeoutMs          int32           `json:"timeout_ms"`
	RateLimitPerSecond *int32          `json:"rate_limit_per_second"` // nil = unlimited
	MaxAttempts        int32           `json:"max_attempts"`
	BackoffBaseSeconds int32           `json:"backoff_base_seconds"`
	BackoffMaxSeconds  int32           `json:"backoff_max_seconds"`
}

type destinationResponse struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	URL                string          `json:"url"`
	AuthConfig         json.RawMessage `json:"auth_config"`
	TimeoutMs          int32           `json:"timeout_ms"`
	RateLimitPerSecond *int32          `json:"rate_limit_per_second"`
	MaxAttempts        int32           `json:"max_attempts"`
	BackoffBaseSeconds int32           `json:"backoff_base_seconds"`
	BackoffMaxSeconds  int32           `json:"backoff_max_seconds"`
	Paused             bool            `json:"paused"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

// decodeDestinationRequest validates a create/update body and fills in
// defaults for any zero-valued numeric field.
func decodeDestinationRequest(r *http.Request) (destinationRequest, string, bool) {
	var req destinationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, "invalid JSON body", false
	}
	if req.Name == "" {
		return req, "name is required", false
	}
	parsed, err := url.Parse(req.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return req, "url must be a valid http(s) URL", false
	}
	if req.AuthConfig == nil {
		req.AuthConfig = []byte("{}")
	}
	if req.TimeoutMs == 0 {
		req.TimeoutMs = defaultTimeoutMs
	}
	if req.MaxAttempts == 0 {
		req.MaxAttempts = defaultMaxAttempts
	}
	if req.BackoffBaseSeconds == 0 {
		req.BackoffBaseSeconds = defaultBackoffBaseSeconds
	}
	if req.BackoffMaxSeconds == 0 {
		req.BackoffMaxSeconds = defaultBackoffMaxSeconds
	}
	if req.RateLimitPerSecond != nil && *req.RateLimitPerSecond <= 0 {
		return req, "rate_limit_per_second must be positive if set", false
	}
	return req, "", true
}

func (h *destinationsHandler) create(w http.ResponseWriter, r *http.Request) {
	req, errMsg, ok := decodeDestinationRequest(r)
	if !ok {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}

	dest, err := h.q.InsertDestination(r.Context(), db.InsertDestinationParams{
		TenantID:           tenancy.DefaultTenantID,
		Name:               req.Name,
		Url:                req.URL,
		AuthConfig:         req.AuthConfig,
		TimeoutMs:          req.TimeoutMs,
		RateLimitPerSecond: int32PtrToPgtype(req.RateLimitPerSecond),
		MaxAttempts:        req.MaxAttempts,
		BackoffBaseSeconds: req.BackoffBaseSeconds,
		BackoffMaxSeconds:  req.BackoffMaxSeconds,
	})
	if err != nil {
		slog.Error("inserting destination", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, toDestinationResponse(dest))
}

func (h *destinationsHandler) list(w http.ResponseWriter, r *http.Request) {
	dests, err := h.q.ListDestinations(r.Context(), tenancy.DefaultTenantID)
	if err != nil {
		slog.Error("listing destinations", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]destinationResponse, len(dests))
	for i, d := range dests {
		out[i] = toDestinationResponse(d)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *destinationsHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid destination id")
		return
	}
	dest, err := h.q.GetDestination(r.Context(), db.GetDestinationParams{ID: id, TenantID: tenancy.DefaultTenantID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "destination not found")
		return
	}
	if err != nil {
		slog.Error("getting destination", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, toDestinationResponse(dest))
}

func (h *destinationsHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid destination id")
		return
	}
	req, errMsg, ok := decodeDestinationRequest(r)
	if !ok {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}

	dest, err := h.q.UpdateDestination(r.Context(), db.UpdateDestinationParams{
		ID:                 id,
		TenantID:           tenancy.DefaultTenantID,
		Name:               req.Name,
		Url:                req.URL,
		AuthConfig:         req.AuthConfig,
		TimeoutMs:          req.TimeoutMs,
		RateLimitPerSecond: int32PtrToPgtype(req.RateLimitPerSecond),
		MaxAttempts:        req.MaxAttempts,
		BackoffBaseSeconds: req.BackoffBaseSeconds,
		BackoffMaxSeconds:  req.BackoffMaxSeconds,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "destination not found")
		return
	}
	if err != nil {
		slog.Error("updating destination", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, toDestinationResponse(dest))
}

func (h *destinationsHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid destination id")
		return
	}
	n, err := h.q.DeleteDestination(r.Context(), db.DeleteDestinationParams{ID: id, TenantID: tenancy.DefaultTenantID})
	if err != nil {
		slog.Error("deleting destination", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "destination not found")
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (h *destinationsHandler) pause(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid destination id")
		return
	}
	dest, err := h.q.PauseDestination(r.Context(), db.PauseDestinationParams{ID: id, TenantID: tenancy.DefaultTenantID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "destination not found")
		return
	}
	if err != nil {
		slog.Error("pausing destination", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, toDestinationResponse(dest))
}

func (h *destinationsHandler) resume(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid destination id")
		return
	}
	dest, err := h.q.ResumeDestination(r.Context(), db.ResumeDestinationParams{ID: id, TenantID: tenancy.DefaultTenantID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "destination not found")
		return
	}
	if err != nil {
		slog.Error("resuming destination", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, toDestinationResponse(dest))
}

func toDestinationResponse(d db.Destination) destinationResponse {
	return destinationResponse{
		ID:                 uuidString(d.ID),
		Name:               d.Name,
		URL:                d.Url,
		AuthConfig:         d.AuthConfig,
		TimeoutMs:          d.TimeoutMs,
		RateLimitPerSecond: pgtypeToInt32Ptr(d.RateLimitPerSecond),
		MaxAttempts:        d.MaxAttempts,
		BackoffBaseSeconds: d.BackoffBaseSeconds,
		BackoffMaxSeconds:  d.BackoffMaxSeconds,
		Paused:             d.PausedAt.Valid,
		CreatedAt:          d.CreatedAt.Time,
		UpdatedAt:          d.UpdatedAt.Time,
	}
}

func int32PtrToPgtype(p *int32) pgtype.Int4 {
	if p == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *p, Valid: true}
}

func pgtypeToInt32Ptr(v pgtype.Int4) *int32 {
	if !v.Valid {
		return nil
	}
	return &v.Int32
}
