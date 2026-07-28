package alerting

// Evaluator tests against the compose Postgres. Skipped unless
// TEST_DATABASE_URL is set:
//
//	make test-integration
//
// These cover the half of BR-19 the pipeline test doesn't: the failure-rate
// condition, its small-sample and window guards, and the rule that a failed
// notification must not start a cooldown. The dead-letter condition is also
// exercised end-to-end through a real worker by TestPipelineAlertOnDeadLetter
// in internal/delivery; the version here isolates the evaluator from it.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"webhook-gateway/internal/db"
	"webhook-gateway/internal/tenancy"
)

func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	// goose up is idempotent, so this no-ops against an already-migrated DB.
	if err := db.Migrate(ctx, dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func randomHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random: %v", err)
	}
	return hex.EncodeToString(b)
}

// fixture is one destination with its own source and event, so each test's
// deliveries are isolated from every other test's in the shared database.
type fixture struct {
	pool     *pgxpool.Pool
	q        *db.Queries
	destID   pgtype.UUID
	destName string
	eventID  pgtype.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testDB(t)
	q := db.New(pool)
	ctx := context.Background()
	suffix := randomHex(t)

	src, err := q.InsertSource(ctx, db.InsertSourceParams{
		TenantID:           tenancy.DefaultTenantID,
		Name:               "alerting-src-" + suffix,
		ProviderType:       "none",
		EndpointPath:       "src_alerting_" + suffix,
		VerificationConfig: []byte("{}"),
	})
	if err != nil {
		t.Fatalf("insert source: %v", err)
	}
	// Deleting the source cascades to its events; deleting the destination
	// cascades to deliveries and alert_state.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM sources WHERE id = $1", src.ID)
	})

	dest, err := q.InsertDestination(ctx, db.InsertDestinationParams{
		TenantID:           tenancy.DefaultTenantID,
		Name:               "alerting-dest-" + suffix,
		Url:                "https://example.com/hook",
		AuthConfig:         []byte("{}"),
		TimeoutMs:          5000,
		MaxAttempts:        3,
		BackoffBaseSeconds: 1,
		BackoffMaxSeconds:  60,
	})
	if err != nil {
		t.Fatalf("insert destination: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM destinations WHERE id = $1", dest.ID)
	})

	var eventID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO events (tenant_id, source_id, raw_headers, raw_body, verified)
		 VALUES ($1, $2, '{}', '{}', true) RETURNING id`,
		tenancy.DefaultTenantID, src.ID,
	).Scan(&eventID); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	return &fixture{pool: pool, q: q, destID: dest.ID, destName: dest.Name, eventID: eventID}
}

// insertDeliveries writes n terminal deliveries. updatedAt drives the window
// filter; dead-lettered rows carry a matching dead_lettered_at, exactly as the
// worker writes them.
func (f *fixture) insertDeliveries(t *testing.T, n int, status string, updatedAt time.Time) {
	t.Helper()
	deadLetteredAt := pgtype.Timestamptz{}
	if status == "dead_lettered" {
		deadLetteredAt = pgtype.Timestamptz{Time: updatedAt, Valid: true}
	}
	for range n {
		if _, err := f.pool.Exec(context.Background(),
			`INSERT INTO deliveries (tenant_id, event_id, destination_id, status, updated_at, dead_lettered_at)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			tenancy.DefaultTenantID, f.eventID, f.destID, status,
			pgtype.Timestamptz{Time: updatedAt, Valid: true}, deadLetteredAt,
		); err != nil {
			t.Fatalf("insert delivery: %v", err)
		}
	}
}

func (f *fixture) id() string { return uuidString(f.destID) }

// alertStateExists reports whether a cooldown has been recorded for this
// destination and condition.
func (f *fixture) alertStateExists(t *testing.T, condition string) bool {
	t.Helper()
	_, err := f.q.GetAlertLastFired(context.Background(), db.GetAlertLastFiredParams{
		DestinationID: f.destID,
		Condition:     condition,
	})
	switch {
	case err == nil:
		return true
	case errors.Is(err, pgx.ErrNoRows):
		return false
	default:
		t.Fatalf("reading alert state: %v", err)
		return false
	}
}

func testConfig() Config {
	return Config{
		CooldownMinutes:  60,
		WindowMinutes:    15,
		FailureThreshold: 0.5,
		MinDeliveries:    5,
	}
}

func TestEvaluatorFailureRateFiresAboveThreshold(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	f.insertDeliveries(t, 6, "dead_lettered", now)
	f.insertDeliveries(t, 4, "succeeded", now)

	notifier := &fakeNotifier{}
	if err := NewEvaluator(f.q, testConfig(), notifier).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	alerts := notifier.forCondition(f.id(), "failure_rate")
	if len(alerts) != 1 {
		t.Fatalf("failure_rate alerts = %d, want 1", len(alerts))
	}
	if alerts[0].DestinationName != f.destName {
		t.Errorf("destination name = %q, want %q", alerts[0].DestinationName, f.destName)
	}
	// 6 of 10 terminal deliveries failed; the detail is what an operator reads.
	if !strings.Contains(alerts[0].Detail, "60%") || !strings.Contains(alerts[0].Detail, "6/10") {
		t.Errorf("detail = %q, want it to report 60%% (6/10)", alerts[0].Detail)
	}
	if !f.alertStateExists(t, "failure_rate") {
		t.Error("no cooldown recorded after a successful notification")
	}
}

// MinDeliveries guards the ratio against small samples: three failures out of
// three is 100%, but it is not yet evidence of anything.
func TestEvaluatorFailureRateIgnoresSmallSamples(t *testing.T) {
	f := newFixture(t)
	f.insertDeliveries(t, 3, "dead_lettered", time.Now())

	notifier := &fakeNotifier{}
	if err := NewEvaluator(f.q, testConfig(), notifier).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := notifier.forCondition(f.id(), "failure_rate"); len(got) != 0 {
		t.Errorf("failure_rate alerts = %d, want 0 below MinDeliveries", len(got))
	}
}

func TestEvaluatorFailureRateBelowThresholdDoesNotFire(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	f.insertDeliveries(t, 2, "dead_lettered", now)
	f.insertDeliveries(t, 8, "succeeded", now)

	notifier := &fakeNotifier{}
	if err := NewEvaluator(f.q, testConfig(), notifier).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := notifier.forCondition(f.id(), "failure_rate"); len(got) != 0 {
		t.Errorf("failure_rate alerts = %d, want 0 at 20%% failure", len(got))
	}
}

// The rate is a moving window: yesterday's outage must not alert today.
func TestEvaluatorFailureRateIgnoresDeliveriesOutsideWindow(t *testing.T) {
	f := newFixture(t)
	f.insertDeliveries(t, 10, "dead_lettered", time.Now().Add(-time.Hour))

	notifier := &fakeNotifier{}
	if err := NewEvaluator(f.q, testConfig(), notifier).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := len(notifier.forCondition(f.id(), "failure_rate")); got != 0 {
		t.Errorf("failure_rate alerts = %d, want 0 outside the window", got)
	}
	if got := len(notifier.forCondition(f.id(), "dlq")); got != 0 {
		t.Errorf("dlq alerts = %d, want 0 outside the window", got)
	}
}

// A threshold of 0 disables the condition outright, even at total failure.
func TestEvaluatorFailureRateDisabledByZeroThreshold(t *testing.T) {
	f := newFixture(t)
	f.insertDeliveries(t, 10, "dead_lettered", time.Now())

	cfg := testConfig()
	cfg.FailureThreshold = 0

	notifier := &fakeNotifier{}
	if err := NewEvaluator(f.q, cfg, notifier).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := notifier.forCondition(f.id(), "failure_rate"); len(got) != 0 {
		t.Errorf("failure_rate alerts = %d, want 0 when the threshold is 0", len(got))
	}
}

func TestEvaluatorDeadLetterCondition(t *testing.T) {
	f := newFixture(t)
	f.insertDeliveries(t, 1, "dead_lettered", time.Now())

	notifier := &fakeNotifier{}
	if err := NewEvaluator(f.q, testConfig(), notifier).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	alerts := notifier.forCondition(f.id(), "dlq")
	if len(alerts) != 1 {
		t.Fatalf("dlq alerts = %d, want 1", len(alerts))
	}
	if !strings.Contains(alerts[0].Detail, f.destName) {
		t.Errorf("detail = %q, want it to name the destination", alerts[0].Detail)
	}
}

// BR-19's cooldown: a bad hour produces one alert, not hundreds.
func TestEvaluatorCooldownSuppressesRepeatAlerts(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	f.insertDeliveries(t, 6, "dead_lettered", now)
	f.insertDeliveries(t, 4, "succeeded", now)

	notifier := &fakeNotifier{}
	evaluator := NewEvaluator(f.q, testConfig(), notifier)
	for range 3 {
		if err := evaluator.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}

	for _, condition := range []string{"failure_rate", "dlq"} {
		if got := notifier.forCondition(f.id(), condition); len(got) != 1 {
			t.Errorf("%s alerts across three runs = %d, want 1 (cooldown)", condition, len(got))
		}
	}
}

// The fire is recorded only after a successful notify, so a Slack outage
// doesn't silently consume the alert: the next run tries again.
func TestEvaluatorRetriesAfterFailedNotify(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	f.insertDeliveries(t, 6, "dead_lettered", now)
	f.insertDeliveries(t, 4, "succeeded", now)

	notifier := &fakeNotifier{err: errors.New("slack is down")}
	evaluator := NewEvaluator(f.q, testConfig(), notifier)

	if err := evaluator.Run(context.Background()); err == nil {
		t.Fatal("Run = nil, want the notify failure surfaced to the periodic job")
	}
	if f.alertStateExists(t, "failure_rate") {
		t.Fatal("cooldown recorded despite the notification failing")
	}

	notifier.setErr(nil)
	if err := evaluator.Run(context.Background()); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	// Two attempts total — the failed one and the successful retry.
	if got := notifier.forCondition(f.id(), "failure_rate"); len(got) != 2 {
		t.Errorf("failure_rate attempts = %d, want 2 (failed, then retried)", len(got))
	}
	if !f.alertStateExists(t, "failure_rate") {
		t.Error("no cooldown recorded after the retry succeeded")
	}
}
