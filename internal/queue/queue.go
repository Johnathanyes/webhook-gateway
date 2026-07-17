// Package queue wires River (a Postgres-backed job queue) into the gateway.
// River is the reason there's no Redis/Kafka dependency: a delivery job is
// enqueued in the *same* Postgres transaction as the event write, so an
// event and its delivery job are durably committed together or not at all
// (the ack-then-process guarantee from the architecture doc).
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

// Migrate creates/updates River's own tables (river_job, river_leader, ...).
// River manages its schema separately from our goose migrations so version
// upgrades are handled by River itself and its tables stay out of sqlc's
// view. Call this on boot, alongside db.Migrate.
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

// DeliveryArgs is the job enqueued for each (event, destination) pair that
// needs to be delivered. It carries only the delivery row's id;
type DeliveryArgs struct {
	DeliveryID string `json:"delivery_id"`
}

// Kind is the stable job type name River persists in river_job.kind; changing
// it would orphan already-enqueued jobs, so it is fixed.
func (DeliveryArgs) Kind() string { return "delivery" }

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
