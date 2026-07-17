package delivery

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"webhook-gateway/internal/db"
	"webhook-gateway/internal/queue"
)

// Worker executes a single delivery job: it resolves the job to its durable
// delivery row and pushes the event to the destination
type Worker struct {
	river.WorkerDefaults[queue.DeliveryArgs]
	q *db.Queries
}

// NewWorker builds a delivery Worker over the shared Queries.
func NewWorker(q *db.Queries) *Worker {
	return &Worker{q: q}
}

// Skeleton entry point. Returning nil marks the job completed;
// that is safe today because nothing enqueues delivery jobs yet (the
// transactional enqueue is the next task). Real dispatch replaces this body.
func (w *Worker) Work(_ context.Context, job *river.Job[queue.DeliveryArgs]) error {
	slog.Info("delivery job received", "delivery_id", job.Args.DeliveryID, "attempt", job.Attempt)
	return nil
}

// Builds the work-capable River client for the worker role
func NewClient(pool *pgxpool.Pool, q *db.Queries) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, NewWorker(q))
	return queue.NewWorkerClient(pool, workers)
}
