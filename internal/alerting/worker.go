package alerting

import (
	"context"
	"log/slog"

	"github.com/riverqueue/river"

	"webhook-gateway/internal/db"
	"webhook-gateway/internal/queue"
)

// CheckWorker runs one alert evaluation when the periodic AlertCheckArgs job
// fires. Reloads the config each run.
type CheckWorker struct {
	river.WorkerDefaults[queue.AlertCheckArgs]
	q *db.Queries
}

func NewCheckWorker(q *db.Queries) *CheckWorker {
	return &CheckWorker{q: q}
}

func (w *CheckWorker) Work(ctx context.Context, _ *river.Job[queue.AlertCheckArgs]) error {
	cfg, err := LoadConfig(ctx, w.q)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	notifier := buildNotifier(cfg)
	if notifier == nil {
		slog.Warn("alerting enabled but no notifier configured; skipping")
		return nil
	}
	return NewEvaluator(w.q, cfg, notifier).Run(ctx)
}
