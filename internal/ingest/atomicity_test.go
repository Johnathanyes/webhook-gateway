package ingest_test

// TestIngestTransactionalEnqueue checks that for a single sequential request.
// What it can't see is what concurrency does to the claim — partial commits,
// interleaved fan-out, or a delivery row whose job never made it. This asserts
// the invariant over the whole set instead of one row: after N concurrent
// ingests, every event has its full complement of deliveries, and every
// delivery points at a River job that actually exists.

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"webhook-gateway/internal/db"
	"webhook-gateway/internal/ingest"
)

func countQuery(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("counting: %v", err)
	}
	return n
}

func TestIngestConcurrentFanOutIsAtomic(t *testing.T) {
	pool := testDB(t)
	enc := testEncryptor(t)
	catalog := testCatalog(t)
	q := db.New(pool)

	secret := []byte("atomicity-secret")
	source := createGithubSource(t, pool, enc, secret)

	// Two enabled routes and one disabled: each event must produce exactly two
	// deliveries, so a miscount is visible rather than averaged away.
	destA := createDestination(t, pool)
	destB := createDestination(t, pool)
	disabled := createDestination(t, pool)
	createRoute(t, pool, source.ID, destA.ID, true)
	createRoute(t, pool, source.ID, destB.ID, true)
	createRoute(t, pool, source.ID, disabled.ID, false)

	mux := http.NewServeMux()
	ingest.Register(mux, pool, q, testRiverClient(t, pool), enc, catalog, generousOpts)

	const (
		n                 = 20
		deliveriesPerEvent = 2
	)
	body := []byte(`{"action":"opened"}`)
	sig := githubSignature(secret, body)

	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := post(mux, source.EndpointPath, body, map[string]string{
				"Content-Type":        "application/json",
				"X-Hub-Signature-256": sig,
			})
			codes[i] = rec.Code
		}()
	}
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i, code)
		}
	}

	if got := eventCount(t, pool, source.ID); got != n {
		t.Errorf("stored events = %d, want %d", got, n)
	}
	if got := deliveryCount(t, pool, source.ID); got != n*deliveriesPerEvent {
		t.Errorf("deliveries = %d, want %d (two enabled routes per event)", got, n*deliveriesPerEvent)
	}

	// No event committed with a partial fan-out. A count other than two means
	// the event and its deliveries did not commit together.
	partial := countQuery(t, pool, `
		SELECT count(*) FROM events e
		WHERE e.source_id = $1
		  AND (SELECT count(*) FROM deliveries d WHERE d.event_id = e.id) <> $2`,
		source.ID, deliveriesPerEvent)
	if partial != 0 {
		t.Errorf("%d events committed with the wrong number of deliveries; fan-out is not atomic", partial)
	}

	// No delivery without a committed River job. A NULL or dangling
	// river_job_id is a delivery nothing will ever pick up — an event accepted
	// with a 200 that is silently never delivered.
	orphans := countQuery(t, pool, `
		SELECT count(*) FROM deliveries d
		JOIN events e ON e.id = d.event_id
		WHERE e.source_id = $1
		  AND (d.river_job_id IS NULL
		       OR NOT EXISTS (SELECT 1 FROM river_job j WHERE j.id = d.river_job_id))`,
		source.ID)
	if orphans != 0 {
		t.Errorf("%d deliveries have no corresponding river_job; those events would never be delivered", orphans)
	}
}
