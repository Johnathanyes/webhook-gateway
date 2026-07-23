package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"webhook-gateway/internal/api/middleware"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/queue"
)

// deliveryStatusDeadLettered mirrors the worker's terminal status; recovery is
// only valid from this state. The DB's status column is the shared contract.
const deliveryStatusDeadLettered = "dead_lettered"

// RegisterDeliveries mounts the delivery recovery API, Insert only River client
func RegisterDeliveries(mux *http.ServeMux, pool *pgxpool.Pool, q *db.Queries, riverClient *river.Client[pgx.Tx], authz *middleware.Auth) {
	h := &deliveriesHandler{pool: pool, q: q, river: riverClient}
		mux.Handle("POST /api/deliveries/{id}/recover", authz.RequireScope(middleware.ScopeReplay, http.HandlerFunc(h.recover)))
}

type deliveriesHandler struct {
	pool  *pgxpool.Pool
	q     *db.Queries
	river *river.Client[pgx.Tx]
}

// recover requeues a single dead-lettered delivery for a fresh set of attempts.
// It resets the delivery row and enqueues a new River job atomically,
// so a committed reset always has a job driving it.
func (h *deliveriesHandler) recover(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid delivery id")
		return
	}
	ctx := r.Context()

	delivery, err := h.q.GetDelivery(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "delivery not found")
		return
	}
	if err != nil {
		slog.Error("loading delivery for recovery", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if delivery.Status != deliveryStatusDeadLettered {
		writeError(w, http.StatusConflict, "delivery is not dead-lettered")
		return
	}

	// The destination's current retry policy is snapshotted onto the new job,
	// same as the original enqueue.
	dest, err := h.q.GetDestination(ctx, db.GetDestinationParams{ID: delivery.DestinationID, TenantID: delivery.TenantID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "destination no longer exists")
		return
	}
	if err != nil {
		slog.Error("loading destination for recovery", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		slog.Error("beginning recovery transaction", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.q.WithTx(tx)

	if err := qtx.ResetDeliveryForRecovery(ctx, id); err != nil {
		slog.Error("resetting delivery for recovery", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	jobID, err := queue.InsertDeliveryJob(ctx, h.river, tx, queue.DeliveryArgs{
		DeliveryID:         uuidString(id),
		BackoffBaseSeconds: dest.BackoffBaseSeconds,
		BackoffMaxSeconds:  dest.BackoffMaxSeconds,
	}, int(dest.MaxAttempts))
	if err != nil {
		slog.Error("re-enqueuing recovered delivery", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := qtx.SetDeliveryRiverJobID(ctx, db.SetDeliveryRiverJobIDParams{
		ID:         id,
		RiverJobID: pgtype.Int8{Int64: jobID, Valid: true},
	}); err != nil {
		slog.Error("linking recovered delivery to job", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("committing recovery", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"delivery_id": uuidString(id), "status": "pending"})
}
