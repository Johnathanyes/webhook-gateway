// Package queue wires River into gateway
package queue

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// Migrate creates/updates River's own tables.
// River manages its schema separately from our goose migrations
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("creating river migrator: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("running river migrations: %w", err)
	}
	return nil
}

type DeliveryArgs struct {
	DeliveryID         string `json:"delivery_id"`
	BackoffBaseSeconds int32  `json:"backoff_base_seconds"`
	BackoffMaxSeconds  int32  `json:"backoff_max_seconds"`
}

// Kind is the stable job type name River persists in river_job.kind; changing
// it would orphan already-enqueued jobs, so it is fixed.
func (DeliveryArgs) Kind() string { return "delivery" }

// InsertDeliveryJob enqueues one delivery job in the given transaction
// and returns the new river_job id for the caller to backfill onto its delivery
// row
func InsertDeliveryJob(ctx context.Context, client *river.Client[pgx.Tx], tx pgx.Tx, args DeliveryArgs, maxAttempts int) (int64, error) {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	res, err := client.InsertTx(ctx, tx, args, &river.InsertOpts{MaxAttempts: maxAttempts})
	if err != nil {
		return 0, fmt.Errorf("enqueuing delivery job: %w", err)
	}
	return res.Job.ID, nil
}

// NewInsertOnlyClient builds a River client that can only enqueue jobs, not
// work them, ingest uses this.
func NewInsertOnlyClient(pool *pgxpool.Pool) (*river.Client[pgx.Tx], error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return nil, fmt.Errorf("creating river client: %w", err)
	}
	return client, nil
}

// Number of delivery jobs a single worker process
// runs at once. It caps outbound concurrency across all destinations
const WorkerConcurrency = 10

// Builds work-capable River client
func NewWorkerClient(pool *pgxpool.Pool, workers *river.Workers) (*river.Client[pgx.Tx], error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: WorkerConcurrency},
		},
		Workers: workers,
	})
	if err != nil {
		return nil, fmt.Errorf("creating river worker client: %w", err)
	}
	return client, nil
}
