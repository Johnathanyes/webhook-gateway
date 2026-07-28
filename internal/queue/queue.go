// Package queue wires River into gateway
package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"webhook-gateway/internal/db"
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

// DeliveryTarget is the per-destination info needed to enqueue one delivery —
// the common shape behind the default route fan-out and the rule
// action='route' override.
type DeliveryTarget struct {
	DestinationID      pgtype.UUID
	MaxAttempts        int32
	BackoffBaseSeconds int32
	BackoffMaxSeconds  int32
}

// EnqueueDeliveries fans an event out to every enabled route target for its
// source
func EnqueueDeliveries(ctx context.Context, client *river.Client[pgx.Tx], tx pgx.Tx, q *db.Queries, tenantID, sourceID, eventID pgtype.UUID) (int, error) {
	rows, err := q.ListEnabledDeliveryTargetsForSource(ctx, sourceID)
	if err != nil {
		return 0, err
	}
	targets := make([]DeliveryTarget, len(rows))
	for i, r := range rows {
		targets[i] = DeliveryTarget{
			DestinationID:      r.DestinationID,
			MaxAttempts:        r.MaxAttempts,
			BackoffBaseSeconds: r.BackoffBaseSeconds,
			BackoffMaxSeconds:  r.BackoffMaxSeconds,
		}
	}
	return EnqueueDeliveriesTo(ctx, client, tx, q, tenantID, eventID, targets)
}

// EnqueueDeliveriesTo enqueues one delivery per explicit target, bypassing the
// routes join — the fan-out set a rule action='route' decided on.
func EnqueueDeliveriesTo(ctx context.Context, client *river.Client[pgx.Tx], tx pgx.Tx, q *db.Queries, tenantID, eventID pgtype.UUID, targets []DeliveryTarget) (int, error) {
	for _, target := range targets {
		deliveryID, err := q.InsertDelivery(ctx, db.InsertDeliveryParams{
			TenantID:      tenantID,
			EventID:       eventID,
			DestinationID: target.DestinationID,
		})
		if err != nil {
			return 0, err
		}
		jobID, err := InsertDeliveryJob(ctx, client, tx, DeliveryArgs{
			DeliveryID:         uuidString(deliveryID),
			BackoffBaseSeconds: target.BackoffBaseSeconds,
			BackoffMaxSeconds:  target.BackoffMaxSeconds,
		}, int(target.MaxAttempts))
		if err != nil {
			return 0, err
		}
		if err := q.SetDeliveryRiverJobID(ctx, db.SetDeliveryRiverJobIDParams{
			ID:         deliveryID,
			RiverJobID: pgtype.Int8{Int64: jobID, Valid: true},
		}); err != nil {
			return 0, err
		}
	}
	return len(targets), nil
}

// ReplayFilter is the JSON stored in replays.filter and the surface a bulk
// replay selects events by.
type ReplayFilter struct {
	SourceID       *string    `json:"source_id,omitempty"`
	Verified       *bool      `json:"verified,omitempty"`
	After          *time.Time `json:"after,omitempty"`
	Before         *time.Time `json:"before,omitempty"`
	Search         *string    `json:"search,omitempty"`
	DeliveryStatus *string    `json:"delivery_status,omitempty"`
}

// ReplayArgs drives a bulk replay in the background. Only the replay id rides
// in the job; the filter and counts live on the replays row so a large replay
// stays observable while it runs.
type ReplayArgs struct {
	ReplayID string `json:"replay_id"`
}

// Kind is the stable job type name persisted in river_job.kind.
func (ReplayArgs) Kind() string { return "replay" }

// InsertReplayJob enqueues one bulk-replay job in tx
func InsertReplayJob(ctx context.Context, client *river.Client[pgx.Tx], tx pgx.Tx, args ReplayArgs) (int64, error) {
	res, err := client.InsertTx(ctx, tx, args, &river.InsertOpts{MaxAttempts: 1})
	if err != nil {
		return 0, fmt.Errorf("enqueuing replay job: %w", err)
	}
	return res.Job.ID, nil
}

// AlertCheckArgs drives the periodic alert evaluation. It carries no
// payload — the evaluator reads all state (config, windows, cooldowns) from the
// database on each run.
type AlertCheckArgs struct{}

// Kind is the stable job type name persisted in river_job.kind.
func (AlertCheckArgs) Kind() string { return "alert_check" }

// DedupCleanupArgs drives the periodic prune of expired dedup_index rows
type DedupCleanupArgs struct{}

// Kind is the stable job type name persisted in river_job.kind.
func (DedupCleanupArgs) Kind() string { return "dedup_cleanup" }

// uuidString renders a pgtype.UUID in canonical 8-4-4-4-12 form for the
// delivery-job args.
func uuidString(u pgtype.UUID) string {
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
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

// Builds work-capable River client. periodicJobs may be nil.
func NewWorkerClient(pool *pgxpool.Pool, workers *river.Workers, periodicJobs []*river.PeriodicJob) (*river.Client[pgx.Tx], error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: WorkerConcurrency},
		},
		Workers:      workers,
		PeriodicJobs: periodicJobs,
	})
	if err != nil {
		return nil, fmt.Errorf("creating river worker client: %w", err)
	}
	return client, nil
}
