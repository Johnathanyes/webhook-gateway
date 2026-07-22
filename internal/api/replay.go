package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"webhook-gateway/internal/auth"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/queue"
	"webhook-gateway/internal/tenancy"
)

func RegisterReplay(mux *http.ServeMux, pool *pgxpool.Pool, q *db.Queries, riverClient *river.Client[pgx.Tx], adminPassword string) {
	h := &replayHandler{pool: pool, q: q, river: riverClient}
	mux.Handle("POST /api/events/{id}/replay", auth.AdminOnly(adminPassword, http.HandlerFunc(h.replayEvent)))
	mux.Handle("POST /api/replays", auth.AdminOnly(adminPassword, http.HandlerFunc(h.bulkReplay)))
	mux.Handle("GET /api/replays/{id}", auth.AdminOnly(adminPassword, http.HandlerFunc(h.getReplay)))
}

type replayHandler struct {
	pool  *pgxpool.Pool
	q     *db.Queries
	river *river.Client[pgx.Tx]
}

// replayEvent re-runs an event through the normal transactional-enqueue path
func (h *replayHandler) replayEvent(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid event id")
		return
	}
	ctx := r.Context()

	event, err := h.q.GetEvent(ctx, db.GetEventParams{ID: id, TenantID: tenancy.DefaultTenantID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}
	if err != nil {
		slog.Error("loading event for replay", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		slog.Error("beginning replay transaction", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	n, err := queue.EnqueueDeliveries(ctx, h.river, tx, h.q.WithTx(tx), event.TenantID, event.SourceID, event.ID)
	if err != nil {
		slog.Error("enqueuing replay deliveries", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Error("committing replay", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"event_id":           uuidString(event.ID),
		"deliveries_created": n,
	})
}

type replayResponse struct {
	ID            string          `json:"id"`
	Status        string          `json:"status"`
	Filter        json.RawMessage `json:"filter"`
	MatchedCount  *int32          `json:"matched_count,omitempty"`
	RequeuedCount *int32          `json:"requeued_count,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	CompletedAt   *time.Time      `json:"completed_at,omitempty"`
}

// bulkReplay records a replay against a filter and hands the actual requeue to
// a River job, so a match set of any size doesn't block the request.
func (h *replayHandler) bulkReplay(w http.ResponseWriter, r *http.Request) {
	var filter queue.ReplayFilter
	if err := json.NewDecoder(r.Body).Decode(&filter); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if errMsg, ok := validateReplayFilter(filter); !ok {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	filterJSON, err := json.Marshal(filter)
	if err != nil {
		slog.Error("marshaling replay filter", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	ctx := r.Context()

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		slog.Error("beginning bulk replay transaction", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.q.WithTx(tx)

	// requested_by is NULL under admin-password auth (no user identity yet).
	replay, err := qtx.InsertReplay(ctx, db.InsertReplayParams{
		TenantID: tenancy.DefaultTenantID,
		Filter:   filterJSON,
	})
	if err != nil {
		slog.Error("inserting replay", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if _, err := queue.InsertReplayJob(ctx, h.river, tx, queue.ReplayArgs{ReplayID: uuidString(replay.ID)}); err != nil {
		slog.Error("enqueuing replay job", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Error("committing bulk replay", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusAccepted, replayResponse{
		ID:            uuidString(replay.ID),
		Status:        replay.Status,
		Filter:        json.RawMessage(replay.Filter),
		MatchedCount:  pgtypeToInt32Ptr(replay.MatchedCount),
		RequeuedCount: pgtypeToInt32Ptr(replay.RequeuedCount),
		CreatedAt:     replay.CreatedAt.Time,
		CompletedAt:   pgtimeToPtr(replay.CompletedAt),
	})
}

func (h *replayHandler) getReplay(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid replay id")
		return
	}
	replay, err := h.q.GetReplay(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && replay.TenantID != tenancy.DefaultTenantID) {
		writeError(w, http.StatusNotFound, "replay not found")
		return
	}
	if err != nil {
		slog.Error("getting replay", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, replayResponse{
		ID:            uuidString(replay.ID),
		Status:        replay.Status,
		Filter:        json.RawMessage(replay.Filter),
		MatchedCount:  pgtypeToInt32Ptr(replay.MatchedCount),
		RequeuedCount: pgtypeToInt32Ptr(replay.RequeuedCount),
		CreatedAt:     replay.CreatedAt.Time,
		CompletedAt:   pgtimeToPtr(replay.CompletedAt),
	})
}

// validateReplayFilter rejects a bulk-replay filter with a malformed source_id
// or an unknown delivery_status before it's stored and dispatched.
func validateReplayFilter(f queue.ReplayFilter) (string, bool) {
	if f.SourceID != nil {
		if _, err := parseUUID(*f.SourceID); err != nil {
			return "invalid source_id", false
		}
	}
	if f.DeliveryStatus != nil && !validDeliveryStatuses[*f.DeliveryStatus] {
		return "invalid delivery_status", false
	}
	return "", true
}
