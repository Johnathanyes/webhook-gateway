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

	"webhook-gateway/internal/alerting"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/ingest"
	"webhook-gateway/internal/observability"
	"webhook-gateway/internal/queue"
	"webhook-gateway/internal/tunnel"
)

// Delivery statuses written to deliveries.status
const (
	statusSucceeded    = "succeeded"
	statusFailed       = "failed"
	statusDeadLettered = "dead_lettered"
)

// How long a delivery to a paused destination is snoozed before rechecking.
// A var, not a const, only so integration tests can shorten it; production
// never reassigns it.
var pausedSnoozeInterval = 30 * time.Second

// Worker executes a single delivery job
type Worker struct {
	river.WorkerDefaults[queue.DeliveryArgs]
	pool       *pgxpool.Pool
	q          *db.Queries
	httpClient *http.Client
	pacer      *pacer
	// tunnels resolves tunnel:// destinations to the socket serving them. It is
	// the same registry the tunnel HTTP handler writes to, which is why tunnel
	// delivery requires both to live in one process (see internal/tunnel).
	tunnels *tunnel.Registry
}

func NewWorker(pool *pgxpool.Pool, q *db.Queries, tunnels *tunnel.Registry) *Worker {
	return &Worker{
		pool:       pool,
		q:          q,
		httpClient: &http.Client{},
		pacer:      newPacer(),
		tunnels:    tunnels,
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

	// Reschedule without delivering or burning an attempt.
	if dest.PausedAt.Valid {
		slog.Debug("destination paused; snoozing delivery", "delivery_id", job.Args.DeliveryID)
		return river.JobSnooze(pausedSnoozeInterval)
	}

	// If the destination is over its rate, snooze for exactly the refill wait
	var rps int32
	if dest.RateLimitPerSecond.Valid {
		rps = dest.RateLimitPerSecond.Int32
	}
	if wait := w.pacer.reserve(dest.ID.Bytes, rps); wait > 0 {
		slog.Debug("pacing delivery", "delivery_id", job.Args.DeliveryID, "wait", wait)
		return river.JobSnooze(wait)
	}

	// A tunnel destination is delivered over its WebSocket rather than HTTP, and
	// loads the event itself because it forwards the provider's raw headers too.
	var result attemptResult
	if tunnel.IsURL(dest.Url) {
		result = w.dispatchTunnel(ctx, dest, delivery.EventID, job.Args.DeliveryID)
	} else {
		event, err := w.q.GetEventForDelivery(ctx, delivery.EventID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("event gone; dropping delivery job", "delivery_id", job.Args.DeliveryID)
				return nil
			}
			return fmt.Errorf("loading event for delivery %s: %w", job.Args.DeliveryID, err)
		}
		result = w.dispatch(ctx, dest, event, job.Args.DeliveryID)
	}
	status := deliveryStatus(result, job.Attempt, job.MaxAttempts)

	if err := w.recordAttempt(ctx, deliveryID, status, result); err != nil {
		// The HTTP call may have succeeded, but we couldn't persist the outcome;
		// retry so the delivery state stays truthful.
		return fmt.Errorf("recording delivery attempt %s: %w", job.Args.DeliveryID, err)
	}
	observability.RecordDelivery(status, result.durationMs.Int32)

	switch {
	case result.succeeded:
		return nil
	case !result.retryable:
		return river.JobCancel(fmt.Errorf("delivery %s to %s dead-lettered: %s", job.Args.DeliveryID, dest.Url, result.errMsg.String))
	default:
		// Retryable failure, if final attempt, discard
		return fmt.Errorf("delivery %s to %s failed: %s", job.Args.DeliveryID, dest.Url, result.errMsg.String)
	}
}

// Maps attempt outcome by success, retryable, or nonretryable
func deliveryStatus(r attemptResult, attempt, maxAttempts int) string {
	switch {
	case r.succeeded:
		return statusSucceeded
	case !r.retryable:
		return statusDeadLettered
	case attempt >= maxAttempts:
		return statusDeadLettered
	default:
		return statusFailed
	}
}

// recordAttempt persists the attempt history row and the delivery's new status
// atomically, so the trace and the delivery state never disagree.
func (w *Worker) recordAttempt(ctx context.Context, deliveryID pgtype.UUID, status string, r attemptResult) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := w.q.WithTx(tx)
	attemptNumber, err := qtx.RecordDeliveryOutcome(ctx, db.RecordDeliveryOutcomeParams{
		ID:     deliveryID,
		Status: status,
	})
	if err != nil {
		return err
	}

	if err := qtx.InsertDeliveryAttempt(ctx, db.InsertDeliveryAttemptParams{
		DeliveryID:            deliveryID,
		AttemptNumber:         attemptNumber,
		RequestHeaders:        r.requestHeaders,
		ResponseStatusCode:    r.statusCode,
		ResponseHeaders:       r.responseHeaders,
		ResponseBodyTruncated: r.responseBody,
		Error:                 r.errMsg,
		DurationMs:            r.durationMs,
	}); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// NewClient builds the work-capable River client for the worker role. It
// registers the delivery, replay, and alert-check workers, and schedules the
// alert evaluation to run once a minute. tunnels is the registry shared with
// the tunnel HTTP handler; it may be an empty registry when no tunnel endpoint
// is mounted in this process, in which case tunnel destinations simply never
// resolve.
func NewClient(pool *pgxpool.Pool, q *db.Queries, tunnels *tunnel.Registry) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, NewWorker(pool, q, tunnels))
	river.AddWorker(workers, NewReplayWorker(pool, q))
	river.AddWorker(workers, alerting.NewCheckWorker(q))
	river.AddWorker(workers, ingest.NewCleanupWorker(q))

	periodic := []*river.PeriodicJob{
		river.NewPeriodicJob(
			river.PeriodicInterval(time.Minute),
			func() (river.JobArgs, *river.InsertOpts) { return queue.AlertCheckArgs{}, nil },
			&river.PeriodicJobOpts{},
		),
		// Five minutes matches the default dedupe window: pruning much more
		// often would mostly scan rows that are still live.
		river.NewPeriodicJob(
			river.PeriodicInterval(5*time.Minute),
			func() (river.JobArgs, *river.InsertOpts) { return queue.DedupCleanupArgs{}, nil },
			&river.PeriodicJobOpts{},
		),
	}
	return queue.NewWorkerClient(pool, workers, periodic)
}

// defaultTimeout guards against a destination row with a non-positive
// timeout_ms
const defaultTimeout = 30 * time.Second
