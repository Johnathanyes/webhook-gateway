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

// NewInsertOnlyClient builds a River client that can only enqueue jobs, not
// work them — this is what the ingest role uses. With no Queues configured
// the client never polls for work, so it's safe to run in a process that
// only produces jobs. The worker role will build a full client with
// registered workers in the delivery phase.
func NewInsertOnlyClient(pool *pgxpool.Pool) (*river.Client[pgx.Tx], error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return nil, fmt.Errorf("creating river client: %w", err)
	}
	return client, nil
}
