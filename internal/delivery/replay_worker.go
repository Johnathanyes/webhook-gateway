package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"webhook-gateway/internal/db"
	"webhook-gateway/internal/queue"
)

// Bulk-replay terminal statuses written to replays.status.
const (
	statusReplayCompleted = "completed"
	statusReplayFailed    = "failed"
)

// ReplayWorker executes one bulk replay
type ReplayWorker struct {
	river.WorkerDefaults[queue.ReplayArgs]
	pool *pgxpool.Pool
	q    *db.Queries
}

func NewReplayWorker(pool *pgxpool.Pool, q *db.Queries) *ReplayWorker {
	return &ReplayWorker{pool: pool, q: q}
}

func (w *ReplayWorker) Work(ctx context.Context, job *river.Job[queue.ReplayArgs]) error {
	var replayID pgtype.UUID
	if err := replayID.Scan(job.Args.ReplayID); err != nil {
		slog.Error("replay job has invalid replay id", "replay_id", job.Args.ReplayID, "error", err)
		return nil
	}

	replay, err := w.q.GetReplay(ctx, replayID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("replay row gone; dropping job", "replay_id", job.Args.ReplayID)
			return nil
		}
		return fmt.Errorf("loading replay %s: %w", job.Args.ReplayID, err)
	}

	var filter queue.ReplayFilter
	if err := json.Unmarshal(replay.Filter, &filter); err != nil {
		w.fail(ctx, replayID, 0, 0)
		return fmt.Errorf("decoding replay filter %s: %w", job.Args.ReplayID, err)
	}

	params, err := replayFilterToParams(replay.TenantID, filter)
	if err != nil {
		w.fail(ctx, replayID, 0, 0)
		return fmt.Errorf("building replay filter params %s: %w", job.Args.ReplayID, err)
	}

	events, err := w.q.ListEventsForReplay(ctx, params)
	if err != nil {
		w.fail(ctx, replayID, 0, 0)
		return fmt.Errorf("selecting events for replay %s: %w", job.Args.ReplayID, err)
	}

	// The work-capable client from context can enqueue the delivery jobs that
	// each event's fan-out produces.
	client := river.ClientFromContext[pgx.Tx](ctx)

	var requeued int
	for _, ev := range events {
		n, err := w.replayOne(ctx, client, replay.TenantID, ev.SourceID, ev.ID)
		if err != nil {
			// Persist what we managed before giving up; MaxAttempts=1 means no
			// retry, so this is the final state.
			w.fail(ctx, replayID, int32(len(events)), int32(requeued))
			return fmt.Errorf("replaying event %s: %w", uuidString(ev.ID), err)
		}
		requeued += n
	}

	if err := w.q.FinishReplay(ctx, db.FinishReplayParams{
		ID:            replayID,
		Status:        statusReplayCompleted,
		MatchedCount:  pgtype.Int4{Int32: int32(len(events)), Valid: true},
		RequeuedCount: pgtype.Int4{Int32: int32(requeued), Valid: true},
	}); err != nil {
		return fmt.Errorf("finishing replay %s: %w", job.Args.ReplayID, err)
	}
	slog.Info("bulk replay complete", "replay_id", job.Args.ReplayID, "matched", len(events), "requeued", requeued)
	return nil
}

// replayOne fans a single event out in its own transaction, so one bad event
// doesn't roll back deliveries already created for earlier ones.
func (w *ReplayWorker) replayOne(ctx context.Context, client *river.Client[pgx.Tx], tenantID, sourceID, eventID pgtype.UUID) (int, error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	n, err := queue.EnqueueDeliveries(ctx, client, tx, w.q.WithTx(tx), tenantID, sourceID, eventID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return n, nil
}

// fail marks a replay failed with whatever counts were reached, logging any
// error from the update rather than masking the original failure.
func (w *ReplayWorker) fail(ctx context.Context, replayID pgtype.UUID, matched, requeued int32) {
	if err := w.q.FinishReplay(ctx, db.FinishReplayParams{
		ID:            replayID,
		Status:        statusReplayFailed,
		MatchedCount:  pgtype.Int4{Int32: matched, Valid: true},
		RequeuedCount: pgtype.Int4{Int32: requeued, Valid: true},
	}); err != nil {
		slog.Error("marking replay failed", "replay_id", uuidString(replayID), "error", err)
	}
}

func replayFilterToParams(tenantID pgtype.UUID, f queue.ReplayFilter) (db.ListEventsForReplayParams, error) {
	p := db.ListEventsForReplayParams{TenantID: tenantID}
	if f.SourceID != nil {
		var u pgtype.UUID
		if err := u.Scan(*f.SourceID); err != nil {
			return p, err
		}
		p.SourceID = u
	}
	if f.Verified != nil {
		p.Verified = pgtype.Bool{Bool: *f.Verified, Valid: true}
	}
	if f.After != nil {
		p.After = pgtype.Timestamptz{Time: *f.After, Valid: true}
	}
	if f.Before != nil {
		p.Before = pgtype.Timestamptz{Time: *f.Before, Valid: true}
	}
	if f.Search != nil {
		p.Search = pgtype.Text{String: *f.Search, Valid: true}
	}
	if f.DeliveryStatus != nil {
		p.DeliveryStatus = pgtype.Text{String: *f.DeliveryStatus, Valid: true}
	}
	return p, nil
}

// uuidString renders a pgtype.UUID in canonical 8-4-4-4-12 form.
func uuidString(u pgtype.UUID) string {
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
