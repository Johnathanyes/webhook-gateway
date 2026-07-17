package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"webhook-gateway/internal/auth"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/tenancy"
)

// RegisterRoutes mounts the routes CRUD API on mux, guarded by the admin
// password. A route is the many-to-many binding between a source and a
// destination that gives fan-out/fan-in for free.
func RegisterRoutes(mux *http.ServeMux, q *db.Queries, adminPassword string) {
	h := &routesHandler{q: q}
	mux.Handle("POST /api/routes", auth.AdminOnly(adminPassword, http.HandlerFunc(h.create)))
	mux.Handle("GET /api/routes", auth.AdminOnly(adminPassword, http.HandlerFunc(h.list)))
	mux.Handle("PATCH /api/routes/{id}", auth.AdminOnly(adminPassword, http.HandlerFunc(h.update)))
	mux.Handle("DELETE /api/routes/{id}", auth.AdminOnly(adminPassword, http.HandlerFunc(h.delete)))
}

type routesHandler struct {
	q *db.Queries
}

type createRouteRequest struct {
	SourceID      string `json:"source_id"`
	DestinationID string `json:"destination_id"`
	Enabled       *bool  `json:"enabled"` // nil = true
}

type updateRouteRequest struct {
	Enabled bool `json:"enabled"`
}

type routeResponse struct {
	ID            string    `json:"id"`
	SourceID      string    `json:"source_id"`
	DestinationID string    `json:"destination_id"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
}

func (h *routesHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createRouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	sourceID, err := parseUUID(req.SourceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "source_id must be a valid UUID")
		return
	}
	destinationID, err := parseUUID(req.DestinationID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "destination_id must be a valid UUID")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	route, err := h.q.InsertRoute(r.Context(), db.InsertRouteParams{
		TenantID:      tenancy.DefaultTenantID,
		SourceID:      sourceID,
		DestinationID: destinationID,
		Enabled:       enabled,
	})
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "route already exists for this source/destination pair")
		return
	}
	if isForeignKeyViolation(err) {
		writeError(w, http.StatusBadRequest, "source_id or destination_id does not exist")
		return
	}
	if err != nil {
		slog.Error("inserting route", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, toRouteResponse(route))
}

func (h *routesHandler) list(w http.ResponseWriter, r *http.Request) {
	params := db.ListRoutesParams{TenantID: tenancy.DefaultTenantID}
	if v := r.URL.Query().Get("source_id"); v != "" {
		id, err := parseUUID(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "source_id must be a valid UUID")
			return
		}
		params.SourceID = id
	}
	if v := r.URL.Query().Get("destination_id"); v != "" {
		id, err := parseUUID(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "destination_id must be a valid UUID")
			return
		}
		params.DestinationID = id
	}

	routes, err := h.q.ListRoutes(r.Context(), params)
	if err != nil {
		slog.Error("listing routes", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]routeResponse, len(routes))
	for i, rt := range routes {
		out[i] = toRouteResponse(rt)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *routesHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid route id")
		return
	}
	var req updateRouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	route, err := h.q.UpdateRouteEnabled(r.Context(), db.UpdateRouteEnabledParams{
		ID:       id,
		TenantID: tenancy.DefaultTenantID,
		Enabled:  req.Enabled,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	if err != nil {
		slog.Error("updating route", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, toRouteResponse(route))
}

func (h *routesHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid route id")
		return
	}
	n, err := h.q.DeleteRoute(r.Context(), db.DeleteRouteParams{ID: id, TenantID: tenancy.DefaultTenantID})
	if err != nil {
		slog.Error("deleting route", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func toRouteResponse(rt db.Route) routeResponse {
	return routeResponse{
		ID:            uuidString(rt.ID),
		SourceID:      uuidString(rt.SourceID),
		DestinationID: uuidString(rt.DestinationID),
		Enabled:       rt.Enabled,
		CreatedAt:     rt.CreatedAt.Time,
	}
}

// Postgres error codes: https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	pgErrUniqueViolation     = "23505"
	pgErrForeignKeyViolation = "23503"
)

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgErrUniqueViolation
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgErrForeignKeyViolation
}
