package ingest_test

// BR-13 against the compose Postgres. Skipped unless TEST_DATABASE_URL is set:
//
//	make test-integration
//
// The unit tests in dedupe_test.go cover key derivation. What matters here is
// the consequence: a duplicate is stored for the audit trail but fans out to
// nothing, and two identical events racing each other produce exactly one
// delivery — never zero, never two.

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"webhook-gateway/internal/crypto"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/ingest"
	"webhook-gateway/internal/tenancy"
)

type dedupeOpts struct {
	strategy      string
	fieldPath     string
	windowSeconds int32
}

// createDedupeSource inserts a "none"-provider source with dedupe configured,
// so these tests exercise dedupe without also signing every request.
func createDedupeSource(t *testing.T, pool *pgxpool.Pool, opts dedupeOpts) db.Source {
	t.Helper()
	if opts.windowSeconds == 0 {
		opts.windowSeconds = 300
	}
	src, err := db.New(pool).InsertSource(context.Background(), db.InsertSourceParams{
		TenantID:            tenancy.DefaultTenantID,
		Name:                "dedupe-test-" + randomHex(t),
		ProviderType:        "none",
		EndpointPath:        "src_dedupe_" + randomHex(t),
		VerificationConfig:  []byte("{}"),
		DedupeEnabled:       opts.strategy != "",
		DedupeStrategy:      pgtype.Text{String: opts.strategy, Valid: opts.strategy != ""},
		DedupeFieldPath:     pgtype.Text{String: opts.fieldPath, Valid: opts.fieldPath != ""},
		DedupeWindowSeconds: opts.windowSeconds,
	})
	if err != nil {
		t.Fatalf("insert source: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, "DELETE FROM events WHERE source_id = $1", src.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM sources WHERE id = $1", src.ID)
	})
	return src
}

// dedupeMux wires a real ingest handler with one enabled route, so "did it fan
// out?" is answerable by counting deliveries.
func dedupeMux(t *testing.T, pool *pgxpool.Pool, src db.Source) *http.ServeMux {
	t.Helper()
	enc, err := crypto.NewEncryptor(testEncryptionKey)
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	dest := createDestination(t, pool)
	createRoute(t, pool, src.ID, dest.ID, true)

	mux := http.NewServeMux()
	ingest.Register(mux, pool, db.New(pool), testRiverClient(t, pool), enc, testCatalog(t), generousOpts)
	return mux
}

func droppedReason(t *testing.T, pool *pgxpool.Pool, eventID pgtype.UUID) string {
	t.Helper()
	var reason pgtype.Text
	if err := pool.QueryRow(context.Background(),
		"SELECT dropped_reason FROM events WHERE id = $1", eventID,
	).Scan(&reason); err != nil {
		t.Fatalf("reading dropped_reason: %v", err)
	}
	return reason.String
}

func storedDedupeKey(t *testing.T, pool *pgxpool.Pool, eventID pgtype.UUID) (string, bool) {
	t.Helper()
	var key pgtype.Text
	if err := pool.QueryRow(context.Background(),
		"SELECT dedupe_key FROM events WHERE id = $1", eventID,
	).Scan(&key); err != nil {
		t.Fatalf("reading dedupe_key: %v", err)
	}
	return key.String, key.Valid
}

func TestIngestDedupeExactWithinWindow(t *testing.T) {
	pool := testDB(t)
	src := createDedupeSource(t, pool, dedupeOpts{strategy: "exact"})
	mux := dedupeMux(t, pool, src)

	body := []byte(`{"id":"evt_dupe","amount":100}`)
	headers := map[string]string{"Content-Type": "application/json"}

	if rec := post(mux, src.EndpointPath, body, headers); rec.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", rec.Code)
	}
	first := latestEventID(t, pool, src.ID)

	// The provider redelivers. It must still get a 200 — a duplicate is not an
	// error, and a non-2xx would make the provider retry forever.
	if rec := post(mux, src.EndpointPath, body, headers); rec.Code != http.StatusOK {
		t.Fatalf("duplicate request status = %d, want 200", rec.Code)
	}
	second := latestEventID(t, pool, src.ID)

	if first == second {
		t.Fatal("the duplicate was not stored; both requests resolved to one event")
	}
	if got := eventCount(t, pool, src.ID); got != 2 {
		t.Errorf("stored events = %d, want 2 (the duplicate is kept for the audit trail)", got)
	}
	if got := droppedReason(t, pool, second); got != "duplicate" {
		t.Errorf("dropped_reason = %q, want %q", got, "duplicate")
	}
	if got := droppedReason(t, pool, first); got != "" {
		t.Errorf("the first event was marked dropped (%q); only the duplicate should be", got)
	}
	if key, ok := storedDedupeKey(t, pool, second); !ok || key == "" {
		t.Error("the duplicate was stored without its dedupe_key, so the log can't explain the drop")
	}

	// The consequence that matters: the duplicate reached no destination.
	if got := deliveryCount(t, pool, src.ID); got != 1 {
		t.Errorf("deliveries = %d, want 1 (only the first event fans out)", got)
	}
}

// A payload that differs by a single byte is a different event.
func TestIngestDedupeExactDistinguishesBodies(t *testing.T) {
	pool := testDB(t)
	src := createDedupeSource(t, pool, dedupeOpts{strategy: "exact"})
	mux := dedupeMux(t, pool, src)
	headers := map[string]string{"Content-Type": "application/json"}

	post(mux, src.EndpointPath, []byte(`{"id":"evt_a"}`), headers)
	post(mux, src.EndpointPath, []byte(`{"id":"evt_b"}`), headers)

	if got := deliveryCount(t, pool, src.ID); got != 2 {
		t.Errorf("deliveries = %d, want 2 (different bodies are different events)", got)
	}
}

// Field strategy keys on identity, not bytes: the same id with a changed
// payload is still the same event, and a different id is not.
func TestIngestDedupeByField(t *testing.T) {
	pool := testDB(t)
	src := createDedupeSource(t, pool, dedupeOpts{strategy: "field", fieldPath: "$.id"})
	mux := dedupeMux(t, pool, src)
	headers := map[string]string{"Content-Type": "application/json"}

	post(mux, src.EndpointPath, []byte(`{"id":"evt_1","attempt":1}`), headers)
	post(mux, src.EndpointPath, []byte(`{"id":"evt_1","attempt":2}`), headers)
	post(mux, src.EndpointPath, []byte(`{"id":"evt_2","attempt":1}`), headers)

	if got := eventCount(t, pool, src.ID); got != 3 {
		t.Errorf("stored events = %d, want 3", got)
	}
	if got := deliveryCount(t, pool, src.ID); got != 2 {
		t.Errorf("deliveries = %d, want 2 (evt_1 once, evt_2 once)", got)
	}
}

// An event whose key can't be derived is delivered rather than dropped: dedupe
// failing open is what keeps a payload shape change from silently swallowing
// traffic.
func TestIngestDedupeMissingFieldFailsOpen(t *testing.T) {
	pool := testDB(t)
	src := createDedupeSource(t, pool, dedupeOpts{strategy: "field", fieldPath: "$.id"})
	mux := dedupeMux(t, pool, src)
	headers := map[string]string{"Content-Type": "application/json"}

	body := []byte(`{"no_id_here":true}`)
	post(mux, src.EndpointPath, body, headers)
	post(mux, src.EndpointPath, body, headers)

	if got := deliveryCount(t, pool, src.ID); got != 2 {
		t.Errorf("deliveries = %d, want 2 (no derivable key means no dedupe)", got)
	}
	if key, ok := storedDedupeKey(t, pool, latestEventID(t, pool, src.ID)); ok {
		t.Errorf("dedupe_key = %q, want NULL when no key could be derived", key)
	}
}

// Past the window the key is reclaimed, so a provider redelivering hours later
// is treated as a new event rather than being silently swallowed.
func TestIngestDedupeWindowExpires(t *testing.T) {
	pool := testDB(t)
	src := createDedupeSource(t, pool, dedupeOpts{strategy: "exact", windowSeconds: 1})
	mux := dedupeMux(t, pool, src)
	headers := map[string]string{"Content-Type": "application/json"}
	body := []byte(`{"id":"evt_window"}`)

	post(mux, src.EndpointPath, body, headers)
	post(mux, src.EndpointPath, body, headers) // inside the window: dropped

	if got := deliveryCount(t, pool, src.ID); got != 1 {
		t.Fatalf("deliveries = %d, want 1 while inside the window", got)
	}

	time.Sleep(1100 * time.Millisecond)

	post(mux, src.EndpointPath, body, headers) // window has passed: delivered
	if got := deliveryCount(t, pool, src.ID); got != 2 {
		t.Errorf("deliveries = %d, want 2 after the window expired", got)
	}
	if got := droppedReason(t, pool, latestEventID(t, pool, src.ID)); got != "" {
		t.Errorf("post-window event dropped_reason = %q, want empty", got)
	}
}

// The race the primary key exists to settle: N identical events at once must
// produce exactly one delivery. Two would defeat dedupe; zero would lose the
// event entirely.
func TestIngestDedupeConcurrentDuplicates(t *testing.T) {
	pool := testDB(t)
	src := createDedupeSource(t, pool, dedupeOpts{strategy: "exact"})
	mux := dedupeMux(t, pool, src)

	const n = 12
	body := []byte(`{"id":"evt_race"}`)

	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = post(mux, src.EndpointPath, body, map[string]string{"Content-Type": "application/json"}).Code
		}()
	}
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("request %d status = %d, want 200", i, code)
		}
	}
	if got := eventCount(t, pool, src.ID); got != n {
		t.Errorf("stored events = %d, want %d", got, n)
	}
	if got := deliveryCount(t, pool, src.ID); got != 1 {
		t.Errorf("deliveries = %d, want exactly 1 across %d concurrent identical events", got, n)
	}

	var dropped int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM events WHERE source_id = $1 AND dropped_reason = 'duplicate'", src.ID,
	).Scan(&dropped); err != nil {
		t.Fatalf("counting duplicates: %v", err)
	}
	if dropped != n-1 {
		t.Errorf("events marked duplicate = %d, want %d", dropped, n-1)
	}
}

// Dedupe is opt-in: a source created without it behaves exactly as before.
func TestIngestDedupeDisabledDeliversEverything(t *testing.T) {
	pool := testDB(t)
	src := createDedupeSource(t, pool, dedupeOpts{})
	mux := dedupeMux(t, pool, src)
	headers := map[string]string{"Content-Type": "application/json"}
	body := []byte(`{"id":"evt_same"}`)

	post(mux, src.EndpointPath, body, headers)
	post(mux, src.EndpointPath, body, headers)

	if got := deliveryCount(t, pool, src.ID); got != 2 {
		t.Errorf("deliveries = %d, want 2 with dedupe disabled", got)
	}
}

// The cleanup job only removes keys past their window; live claims survive it.
func TestDeleteExpiredDedupEntries(t *testing.T) {
	pool := testDB(t)
	q := db.New(pool)

	live := createDedupeSource(t, pool, dedupeOpts{strategy: "exact"})
	expiring := createDedupeSource(t, pool, dedupeOpts{strategy: "exact", windowSeconds: 1})
	liveMux := dedupeMux(t, pool, live)
	expiringMux := dedupeMux(t, pool, expiring)
	headers := map[string]string{"Content-Type": "application/json"}

	post(liveMux, live.EndpointPath, []byte(`{"id":"stays"}`), headers)
	post(expiringMux, expiring.EndpointPath, []byte(`{"id":"goes"}`), headers)

	time.Sleep(1100 * time.Millisecond)
	if _, err := q.DeleteExpiredDedupEntries(context.Background()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	remaining := func(sourceID pgtype.UUID) int {
		var n int
		if err := pool.QueryRow(context.Background(),
			"SELECT count(*) FROM dedup_index WHERE source_id = $1", sourceID,
		).Scan(&n); err != nil {
			t.Fatalf("counting dedup_index: %v", err)
		}
		return n
	}
	if got := remaining(expiring.ID); got != 0 {
		t.Errorf("expired entries remaining = %d, want 0", got)
	}
	if got := remaining(live.ID); got != 1 {
		t.Errorf("live entries remaining = %d, want 1 (cleanup deleted a claim still in its window)", got)
	}
}
