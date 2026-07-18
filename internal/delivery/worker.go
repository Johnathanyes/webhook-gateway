package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"webhook-gateway/internal/db"
	"webhook-gateway/internal/queue"
)

// Delivery statuses written to deliveries.status
const (
	statusSucceeded = "succeeded"
	statusFailed    = "failed"
)

// Worker executes a single delivery job
type Worker struct {
	river.WorkerDefaults[queue.DeliveryArgs]
	pool       *pgxpool.Pool
	q          *db.Queries
	httpClient *http.Client
}

func NewWorker(pool *pgxpool.Pool, q *db.Queries) *Worker {
	return &Worker{
		pool:       pool,
		q:          q,
		httpClient: &http.Client{},
	}
}

// Work delivers one event to one destination. Returning an error marks the job
// for retry, scheduled by NextRetry's per-destination backoff;
// returning nil completes it.
func (w *Worker) Work(ctx context.Context, job *river.Job[queue.DeliveryArgs]) error {
	var deliveryID pgtype.UUID
	if err := deliveryID.Scan(job.Args.DeliveryID); err != nil {
		// A malformed id can never succeed on retry, so complete the job.
		slog.Error("delivery job has invalid delivery id", "delivery_id", job.Args.DeliveryID, "error", err)
		return nil
	}

	delivery, err := w.q.GetDelivery(ctx, deliveryID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("delivery row gone; dropping job", "delivery_id", job.Args.DeliveryID)
			return nil
		}
		return fmt.Errorf("loading delivery %s: %w", job.Args.DeliveryID, err)
	}

	// Don't re-POST if a delivery has already succeeded.
	if delivery.Status == statusSucceeded {
		slog.Info("delivery already succeeded; skipping redelivered job", "delivery_id", job.Args.DeliveryID)
		return nil
	}

	event, err := w.q.GetEventForDelivery(ctx, delivery.EventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("event gone; dropping delivery job", "delivery_id", job.Args.DeliveryID)
			return nil
		}
		return fmt.Errorf("loading event for delivery %s: %w", job.Args.DeliveryID, err)
	}

	dest, err := w.q.GetDestination(ctx, db.GetDestinationParams{
		ID:       delivery.DestinationID,
		TenantID: delivery.TenantID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("destination gone; dropping delivery job", "delivery_id", job.Args.DeliveryID)
			return nil
		}
		return fmt.Errorf("loading destination for delivery %s: %w", job.Args.DeliveryID, err)
	}

	result := w.dispatch(ctx, dest, event)

	if err := w.recordAttempt(ctx, deliveryID, int32(job.Attempt), result); err != nil {
		// The HTTP call may have succeeded, but we couldn't persist the outcome;
		// retry so the delivery state stays truthful.
		return fmt.Errorf("recording delivery attempt %s: %w", job.Args.DeliveryID, err)
	}

	if !result.succeeded {
		return fmt.Errorf("delivery %s to %s failed: %s", job.Args.DeliveryID, dest.Url, result.errMsg.String)
	}
	return nil
}

// recordAttempt persists the attempt history row and the delivery's new status
// atomically, so the trace and the delivery state never disagree.
func (w *Worker) recordAttempt(ctx context.Context, deliveryID pgtype.UUID, attempt int32, r attemptResult) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := w.q.WithTx(tx)
	if err := qtx.InsertDeliveryAttempt(ctx, db.InsertDeliveryAttemptParams{
		DeliveryID:            deliveryID,
		AttemptNumber:         attempt,
		RequestHeaders:        r.requestHeaders,
		ResponseStatusCode:    r.statusCode,
		ResponseHeaders:       r.responseHeaders,
		ResponseBodyTruncated: r.responseBody,
		Error:                 r.errMsg,
		DurationMs:            r.durationMs,
	}); err != nil {
		return err
	}

	status := statusFailed
	if r.succeeded {
		status = statusSucceeded
	}
	if err := qtx.UpdateDeliveryOutcome(ctx, db.UpdateDeliveryOutcomeParams{
		ID:           deliveryID,
		Status:       status,
		AttemptCount: attempt,
	}); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// NewClient builds the work-capable River client for the worker role.
func NewClient(pool *pgxpool.Pool, q *db.Queries) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, NewWorker(pool, q))
	return queue.NewWorkerClient(pool, workers)
}

// defaultTimeout guards against a destination row with a non-positive
// timeout_ms
const defaultTimeout = 30 * time.Second
