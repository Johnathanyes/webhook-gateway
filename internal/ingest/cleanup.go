package ingest

import (
	"context"
	"log/slog"

	"github.com/riverqueue/river"

	"webhook-gateway/internal/db"
	"webhook-gateway/internal/queue"
)

// CleanupWorker prunes dedupe entries past their window when the periodic
// DedupCleanupArgs job fires. Dedup correctness does not depend on it —
// ClaimDedupeKey reclaims an expired row in place — so a missed run costs
// disk, never a wrongly-dropped or wrongly-delivered event.
type CleanupWorker struct {
	river.WorkerDefaults[queue.DedupCleanupArgs]
	q *db.Queries
}

func NewCleanupWorker(q *db.Queries) *CleanupWorker {
	return &CleanupWorker{q: q}
}

func (w *CleanupWorker) Work(ctx context.Context, _ *river.Job[queue.DedupCleanupArgs]) error {
	deleted, err := w.q.DeleteExpiredDedupEntries(ctx)
	if err != nil {
		return err
	}
	if deleted > 0 {
		slog.Info("pruned expired dedupe entries", "rows_deleted", deleted)
	}
	return nil
}
