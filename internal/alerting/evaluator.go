package alerting

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"webhook-gateway/internal/db"
)

// Evaluator checks the alert conditions for one run and dispatches through a
// Notifier
type Evaluator struct {
	q        *db.Queries
	cfg      Config
	notifier Notifier
}

// NewEvaluator builds an evaluator for the given config and notifier. The
// notifier is injected.
func NewEvaluator(q *db.Queries, cfg Config, notifier Notifier) *Evaluator {
	return &Evaluator{q: q, cfg: cfg, notifier: notifier}
}

// Run evaluates both conditions once. Per-destination notify errors are joined
// and returned so the periodic job records them, but they don't stop the other
// destinations from being processed.
func (e *Evaluator) Run(ctx context.Context) error {
	if e.notifier == nil {
		// Nothing configured to notify; conditions would fire into the void.
		return nil
	}
	now := time.Now()
	windowStart := pgtype.Timestamptz{Time: now.Add(-time.Duration(e.cfg.WindowMinutes) * time.Minute), Valid: true}

	var errs []error

	// Condition 1: any delivery dead-lettered within the window.
	dlq, err := e.q.DeadLetteredSince(ctx, windowStart)
	if err != nil {
		return fmt.Errorf("querying dead-letters: %w", err)
	}
	for _, d := range dlq {
		detail := fmt.Sprintf("A delivery to %q dead-lettered within the last %d minutes.", d.Name, e.cfg.WindowMinutes)
		if err := e.maybeFire(ctx, now, d.DestinationID, d.Name, "dlq", detail); err != nil {
			errs = append(errs, err)
		}
	}

	// Condition 2: per-destination failure rate over the window.
	if e.cfg.FailureThreshold > 0 {
		rates, err := e.q.DeliveryFailureRatesSince(ctx, windowStart)
		if err != nil {
			return fmt.Errorf("querying failure rates: %w", err)
		}
		for _, r := range rates {
			if r.Total < int64(e.cfg.MinDeliveries) {
				continue
			}
			rate := float64(r.Failures) / float64(r.Total)
			if rate < e.cfg.FailureThreshold {
				continue
			}
			detail := fmt.Sprintf("Failure rate %.0f%% (%d/%d) over %dm exceeds threshold %.0f%%.",
				rate*100, r.Failures, r.Total, e.cfg.WindowMinutes, e.cfg.FailureThreshold*100)
			if err := e.maybeFire(ctx, now, r.DestinationID, r.Name, "failure_rate", detail); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errors.Join(errs...)
}

// maybeFire dispatches an alert unless one for the same destination+condition
// fired within the cooldown. It only records the fire after a successful notify,
// so a failed send is retried on the next run rather than silently cooled down.
func (e *Evaluator) maybeFire(ctx context.Context, now time.Time, destID pgtype.UUID, name, condition, detail string) error {
	last, err := e.q.GetAlertLastFired(ctx, db.GetAlertLastFiredParams{DestinationID: destID, Condition: condition})
	switch {
	case err == nil:
		if now.Sub(last.Time) < time.Duration(e.cfg.CooldownMinutes)*time.Minute {
			return nil // still cooling down
		}
	case errors.Is(err, pgx.ErrNoRows):
		// Never fired for this pair; proceed.
	default:
		return fmt.Errorf("reading alert state: %w", err)
	}

	alert := Alert{
		Condition:       condition,
		DestinationID:   uuidString(destID),
		DestinationName: name,
		Detail:          detail,
		FiredAt:         now,
	}
	if err := e.notifier.Notify(ctx, alert); err != nil {
		return fmt.Errorf("notifying %s alert for %s: %w", condition, name, err)
	}

	return e.q.MarkAlertFired(ctx, db.MarkAlertFiredParams{
		DestinationID: destID,
		Condition:     condition,
		LastFiredAt:   pgtype.Timestamptz{Time: now, Valid: true},
	})
}

func uuidString(u pgtype.UUID) string {
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
