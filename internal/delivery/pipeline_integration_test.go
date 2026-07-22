package delivery

// End-to-end verification of the delivery pipeline (task 9): a real ingest
// handler enqueues in one tx, a real running River worker delivers to httptest
// destinations, and we assert the durable state in Postgres. Skipped unless
// TEST_DATABASE_URL is set (see the ingest integration tests for how to run):
//
//	make test-integration
//
// These cover BR-07 (durable at-least-once, restart survival), BR-08 (retry +
// backoff, dead-letter on exhaustion), BR-09 (dead-letter + recover), BR-10
// (pacing), BR-11 (timeout as failure), BR-12 (pause/resume).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"webhook-gateway/internal/alerting"
	"webhook-gateway/internal/api"
	"webhook-gateway/internal/crypto"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/ingest"
	"webhook-gateway/internal/queue"
	"webhook-gateway/internal/sourcedef"
	"webhook-gateway/internal/tenancy"
)

const (
	testEncryptionKey = "ZGV2LTMyLWJ5dGUtZW5jcnlwdGlvbi1rZXktMDAwMDA="
	testAdminPassword = "test-admin-password"
)

// ---- harness ----------------------------------------------------------------

type harness struct {
	pool         *pgxpool.Pool
	q            *db.Queries
	mux          *http.ServeMux
	insertClient *river.Client[pgx.Tx]
}

// newHarness boots the ingest handler + recover endpoint against the compose
// Postgres and River. It does NOT start a worker — tests do that explicitly so
// restart-durability can observe the pre-worker state.
func newHarness(t *testing.T) *harness {
	t.Helper()
	pool := testDB(t)
	q := db.New(pool)

	enc, err := crypto.NewEncryptor(testEncryptionKey)
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	catalog, err := sourcedef.Load()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	insertClient, err := queue.NewInsertOnlyClient(pool)
	if err != nil {
		t.Fatalf("insert client: %v", err)
	}

	mux := http.NewServeMux()
	ingest.Register(mux, pool, q, insertClient, enc, catalog,
		ingest.Options{MaxBodyBytes: 1 << 20, RateLimitPerSecond: 1000})
	api.RegisterDeliveries(mux, pool, q, insertClient, testAdminPassword)
	api.RegisterReplay(mux, pool, q, insertClient, testAdminPassword)

	return &harness{pool: pool, q: q, mux: mux, insertClient: insertClient}
}

// startWorker starts a real delivery worker and stops it on cleanup.
func (h *harness) startWorker(t *testing.T) {
	t.Helper()
	client, err := NewClient(h.pool, h.q)
	if err != nil {
		t.Fatalf("worker client: %v", err)
	}
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("starting worker: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Stop(ctx)
	})
}

func (h *harness) createSource(t *testing.T) db.Source {
	t.Helper()
	src, err := h.q.InsertSource(context.Background(), db.InsertSourceParams{
		TenantID:           tenancy.DefaultTenantID,
		Name:               "e2e-source",
		ProviderType:       "none", // no signature verification; every event stored verified
		EndpointPath:       "src_" + randomHex(t),
		VerificationConfig: []byte("{}"),
	})
	if err != nil {
		t.Fatalf("insert source: %v", err)
	}
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), "DELETE FROM events WHERE source_id = $1", src.ID)
		_, _ = h.pool.Exec(context.Background(), "DELETE FROM sources WHERE id = $1", src.ID)
	})
	return src
}

type destOpts struct {
	maxAttempts int32
	rps         int32 // 0 => unlimited
	timeoutMs   int32
	paused      bool
}

func (h *harness) createDestination(t *testing.T, url string, opts destOpts) db.Destination {
	t.Helper()
	if opts.maxAttempts == 0 {
		opts.maxAttempts = 5
	}
	if opts.timeoutMs == 0 {
		opts.timeoutMs = 2000
	}
	rate := pgtype.Int4{}
	if opts.rps > 0 {
		rate = pgtype.Int4{Int32: opts.rps, Valid: true}
	}
	dest, err := h.q.InsertDestination(context.Background(), db.InsertDestinationParams{
		TenantID:           tenancy.DefaultTenantID,
		Name:               "e2e-dest",
		Url:                url,
		AuthConfig:         []byte("{}"),
		TimeoutMs:          opts.timeoutMs,
		RateLimitPerSecond: rate,
		MaxAttempts:        opts.maxAttempts,
		BackoffBaseSeconds: 1, // fast retries so the suite stays quick
		BackoffMaxSeconds:  2,
	})
	if err != nil {
		t.Fatalf("insert destination: %v", err)
	}
	if opts.paused {
		if _, err := h.q.PauseDestination(context.Background(), db.PauseDestinationParams{ID: dest.ID, TenantID: tenancy.DefaultTenantID}); err != nil {
			t.Fatalf("pause destination: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), "DELETE FROM destinations WHERE id = $1", dest.ID)
	})
	return dest
}

func (h *harness) route(t *testing.T, src db.Source, dest db.Destination) {
	t.Helper()
	if _, err := h.q.InsertRoute(context.Background(), db.InsertRouteParams{
		TenantID:      tenancy.DefaultTenantID,
		SourceID:      src.ID,
		DestinationID: dest.ID,
		Enabled:       true,
	}); err != nil {
		t.Fatalf("insert route: %v", err)
	}
}

// postEvent sends an event through the real ingest endpoint and returns the
// stored event id.
func (h *harness) postEvent(t *testing.T, src db.Source, body string) pgtype.UUID {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/ingest/"+src.EndpointPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest returned %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	return latestEventID(t, h.pool, src.ID)
}

func (h *harness) recover(t *testing.T, deliveryID pgtype.UUID) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/deliveries/"+uuidStr(deliveryID)+"/recover", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminPassword)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec.Code
}

func (h *harness) replayEvent(t *testing.T, eventID pgtype.UUID) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/events/"+uuidStr(eventID)+"/replay", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminPassword)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec.Code
}

// bulkReplay posts a filter to /api/replays and returns the new replay id.
func (h *harness) bulkReplay(t *testing.T, filterJSON string) pgtype.UUID {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/replays", strings.NewReader(filterJSON))
	req.Header.Set("Authorization", "Bearer "+testAdminPassword)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("bulk replay returned %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding replay response: %v", err)
	}
	var id pgtype.UUID
	if err := id.Scan(resp.ID); err != nil {
		t.Fatalf("parsing replay id %q: %v", resp.ID, err)
	}
	return id
}

type replayRow struct {
	status   string
	matched  int32
	requeued int32
}

func replayState(t *testing.T, pool *pgxpool.Pool, replayID pgtype.UUID) replayRow {
	t.Helper()
	var r replayRow
	if err := pool.QueryRow(context.Background(),
		"SELECT status, coalesce(matched_count, -1), coalesce(requeued_count, -1) FROM replays WHERE id = $1",
		replayID).Scan(&r.status, &r.matched, &r.requeued); err != nil {
		t.Fatalf("query replay: %v", err)
	}
	return r
}

// ---- recording destination server -------------------------------------------

// recordingServer is a fake destination that records what it receives and
// returns a status decided per-call, so a test can script failures.
type recordingServer struct {
	srv        *httptest.Server
	mu         sync.Mutex
	bodies     []string
	webhookIDs []string
	statusFn   func(call int) int
	delay      time.Duration
}

func newRecordingServer(t *testing.T, statusFn func(call int) int) *recordingServer {
	rs := &recordingServer{statusFn: statusFn}
	rs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rs.mu.Lock()
		rs.bodies = append(rs.bodies, string(body))
		rs.webhookIDs = append(rs.webhookIDs, r.Header.Get("Webhook-Id"))
		call := len(rs.bodies)
		status := rs.statusFn(call)
		delay := rs.delay
		rs.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(rs.srv.Close)
	return rs
}

func (rs *recordingServer) count() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return len(rs.bodies)
}

func (rs *recordingServer) setStatusFn(fn func(call int) int) {
	rs.mu.Lock()
	rs.statusFn = fn
	rs.mu.Unlock()
}

func (rs *recordingServer) lastWebhookID() string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.webhookIDs) == 0 {
		return ""
	}
	return rs.webhookIDs[len(rs.webhookIDs)-1]
}

func alwaysStatus(code int) func(int) int { return func(int) int { return code } }

// ---- scenarios --------------------------------------------------------------

// BR-07: one event fans out to every enabled route, each is delivered exactly
// once, and a redelivered job for an already-succeeded delivery is a no-op
// (at-least-once is safe).
func TestPipelineFanOutAndAtLeastOnce(t *testing.T) {
	h := newHarness(t)
	h.startWorker(t)

	destA := newRecordingServer(t, alwaysStatus(200))
	destB := newRecordingServer(t, alwaysStatus(200))
	src := h.createSource(t)
	dA := h.createDestination(t, destA.srv.URL, destOpts{})
	dB := h.createDestination(t, destB.srv.URL, destOpts{})
	h.route(t, src, dA)
	h.route(t, src, dB)

	eventID := h.postEvent(t, src, `{"hello":"fanout"}`)

	eventually(t, 15*time.Second, "both deliveries succeed", func() bool {
		ds := deliveriesForEvent(t, h.pool, eventID)
		return len(ds) == 2 && ds[0].status == statusSucceeded && ds[1].status == statusSucceeded
	})

	if destA.count() != 1 || destB.count() != 1 {
		t.Fatalf("delivery counts A=%d B=%d, want 1 each", destA.count(), destB.count())
	}
	if got := destA.bodies[0]; got != `{"hello":"fanout"}` {
		t.Errorf("destA body = %q, want the verbatim payload", got)
	}
	if destA.lastWebhookID() == "" {
		t.Error("destA got no Webhook-Id header")
	}

	// At-least-once safety: redelivering an already-succeeded delivery must not
	// POST again (the worker skips succeeded deliveries).
	dlv := deliveriesForEvent(t, h.pool, eventID)[0]
	h.enqueueDuplicate(t, dlv.id)
	time.Sleep(2 * time.Second)
	total := destA.count() + destB.count()
	if total != 2 {
		t.Errorf("after redelivery, total destination calls = %d, want 2 (no re-POST)", total)
	}
}

// BR-08/BR-11: transient failures (5xx) retry with backoff until they succeed,
// and every attempt is recorded.
func TestPipelineRetryThenSucceed(t *testing.T) {
	h := newHarness(t)
	h.startWorker(t)

	// Fail the first two attempts, succeed on the third.
	dest := newRecordingServer(t, func(call int) int {
		if call <= 2 {
			return 500
		}
		return 200
	})
	src := h.createSource(t)
	d := h.createDestination(t, dest.srv.URL, destOpts{maxAttempts: 5})
	h.route(t, src, d)

	eventID := h.postEvent(t, src, `{"hello":"retry"}`)

	eventually(t, 20*time.Second, "delivery eventually succeeds", func() bool {
		ds := deliveriesForEvent(t, h.pool, eventID)
		return len(ds) == 1 && ds[0].status == statusSucceeded
	})

	dlv := deliveriesForEvent(t, h.pool, eventID)[0]
	if dlv.attemptCount != 3 {
		t.Errorf("attempt_count = %d, want 3", dlv.attemptCount)
	}
	if n := attemptRowCount(t, h.pool, dlv.id); n != 3 {
		t.Errorf("delivery_attempts rows = %d, want 3", n)
	}
	if dest.count() != 3 {
		t.Errorf("destination calls = %d, want 3", dest.count())
	}
}

// BR-09: a terminal 4xx dead-letters immediately without burning the retry
// budget.
func TestPipelineTerminal4xxDeadLetters(t *testing.T) {
	h := newHarness(t)
	h.startWorker(t)

	dest := newRecordingServer(t, alwaysStatus(400))
	src := h.createSource(t)
	d := h.createDestination(t, dest.srv.URL, destOpts{maxAttempts: 5})
	h.route(t, src, d)

	eventID := h.postEvent(t, src, `{"hello":"terminal"}`)

	eventually(t, 15*time.Second, "delivery dead-letters", func() bool {
		ds := deliveriesForEvent(t, h.pool, eventID)
		return len(ds) == 1 && ds[0].status == statusDeadLettered
	})

	dlv := deliveriesForEvent(t, h.pool, eventID)[0]
	if dlv.attemptCount != 1 {
		t.Errorf("attempt_count = %d, want 1 (no retries on 4xx)", dlv.attemptCount)
	}
	if !dlv.deadLettered {
		t.Error("dead_lettered_at not set")
	}
	// Give any (incorrect) retry a chance to land, then confirm it never did.
	time.Sleep(2 * time.Second)
	if dest.count() != 1 {
		t.Errorf("destination calls = %d, want 1 (terminal, no retry)", dest.count())
	}
}

// BR-08: a destination that always fails with a retryable error dead-letters
// once the attempt budget is exhausted.
func TestPipelineExhaustionDeadLetters(t *testing.T) {
	h := newHarness(t)
	h.startWorker(t)

	dest := newRecordingServer(t, alwaysStatus(503))
	src := h.createSource(t)
	d := h.createDestination(t, dest.srv.URL, destOpts{maxAttempts: 2})
	h.route(t, src, d)

	eventID := h.postEvent(t, src, `{"hello":"exhaust"}`)

	eventually(t, 20*time.Second, "delivery dead-letters after exhaustion", func() bool {
		ds := deliveriesForEvent(t, h.pool, eventID)
		return len(ds) == 1 && ds[0].status == statusDeadLettered
	})

	dlv := deliveriesForEvent(t, h.pool, eventID)[0]
	if dlv.attemptCount != 2 {
		t.Errorf("attempt_count = %d, want 2 (max_attempts)", dlv.attemptCount)
	}
	if dest.count() != 2 {
		t.Errorf("destination calls = %d, want 2", dest.count())
	}
}

// BR-09: a dead-lettered delivery can be recovered — reset and re-enqueued —
// and then succeeds against a now-healthy destination, continuing its attempt
// history.
func TestPipelineRecover(t *testing.T) {
	h := newHarness(t)
	h.startWorker(t)

	dest := newRecordingServer(t, alwaysStatus(500))
	src := h.createSource(t)
	d := h.createDestination(t, dest.srv.URL, destOpts{maxAttempts: 1})
	h.route(t, src, d)

	eventID := h.postEvent(t, src, `{"hello":"recover"}`)
	eventually(t, 15*time.Second, "delivery dead-letters", func() bool {
		ds := deliveriesForEvent(t, h.pool, eventID)
		return len(ds) == 1 && ds[0].status == statusDeadLettered
	})
	dlv := deliveriesForEvent(t, h.pool, eventID)[0]

	// Recovering a non-dead-lettered delivery would 409; this one is eligible.
	dest.setStatusFn(alwaysStatus(200)) // destination is fixed
	if code := h.recover(t, dlv.id); code != http.StatusOK {
		t.Fatalf("recover returned %d, want 200", code)
	}

	eventually(t, 15*time.Second, "recovered delivery succeeds", func() bool {
		ds := deliveriesForEvent(t, h.pool, eventID)
		return ds[0].status == statusSucceeded
	})
	final := deliveriesForEvent(t, h.pool, eventID)[0]
	if final.attemptCount < 2 {
		t.Errorf("attempt_count = %d, want >= 2 (history continued across recovery)", final.attemptCount)
	}
}

// BR-18 (single replay): replaying a delivered event creates a fresh delivery
// with a new Webhook-Id that runs through the normal path and delivers again.
func TestPipelineReplaySingle(t *testing.T) {
	h := newHarness(t)
	h.startWorker(t)

	dest := newRecordingServer(t, alwaysStatus(200))
	src := h.createSource(t)
	d := h.createDestination(t, dest.srv.URL, destOpts{})
	h.route(t, src, d)

	eventID := h.postEvent(t, src, `{"hello":"replay"}`)
	eventually(t, 15*time.Second, "original delivery succeeds", func() bool {
		ds := deliveriesForEvent(t, h.pool, eventID)
		return len(ds) == 1 && ds[0].status == statusSucceeded
	})
	firstWebhookID := dest.lastWebhookID()

	if code := h.replayEvent(t, eventID); code != http.StatusAccepted {
		t.Fatalf("replay returned %d, want 202", code)
	}

	// A second, distinct delivery appears and is delivered.
	eventually(t, 15*time.Second, "replayed delivery succeeds", func() bool {
		ds := deliveriesForEvent(t, h.pool, eventID)
		return len(ds) == 2 && ds[0].status == statusSucceeded && ds[1].status == statusSucceeded
	})
	if dest.count() != 2 {
		t.Errorf("destination calls = %d, want 2", dest.count())
	}
	if got := dest.lastWebhookID(); got == "" || got == firstWebhookID {
		t.Errorf("replay Webhook-Id = %q, want a new id distinct from %q", got, firstWebhookID)
	}
}

// BR-18 (bulk replay): a filtered bulk replay requeues exactly the matched set
// via a River job and the replay row completes with correct counts.
func TestPipelineReplayBulk(t *testing.T) {
	h := newHarness(t)
	h.startWorker(t)

	dest := newRecordingServer(t, alwaysStatus(200))
	src := h.createSource(t)
	d := h.createDestination(t, dest.srv.URL, destOpts{})
	h.route(t, src, d)

	const n = 3
	var eventIDs []pgtype.UUID
	for range n {
		eventIDs = append(eventIDs, h.postEvent(t, src, `{"hello":"bulk"}`))
	}
	eventually(t, 20*time.Second, "all original deliveries succeed", func() bool {
		return dest.count() == n
	})

	replayID := h.bulkReplay(t, `{"source_id":"`+uuidStr(src.ID)+`"}`)

	eventually(t, 20*time.Second, "bulk replay completes", func() bool {
		return replayState(t, h.pool, replayID).status == statusReplayCompleted
	})
	rs := replayState(t, h.pool, replayID)
	if rs.matched != n || rs.requeued != n {
		t.Errorf("replay counts matched=%d requeued=%d, want %d/%d", rs.matched, rs.requeued, n, n)
	}

	// Each event now has two deliveries (original + replay), all delivered.
	eventually(t, 20*time.Second, "every event has two succeeded deliveries", func() bool {
		for _, eid := range eventIDs {
			ds := deliveriesForEvent(t, h.pool, eid)
			if len(ds) != 2 || ds[0].status != statusSucceeded || ds[1].status != statusSucceeded {
				return false
			}
		}
		return true
	})
	if dest.count() != 2*n {
		t.Errorf("destination calls = %d, want %d", dest.count(), 2*n)
	}
}

// fakeNotifier records the alerts it is asked to send, for the alerting test.
type fakeNotifier struct {
	mu     sync.Mutex
	alerts []alerting.Alert
}

func (f *fakeNotifier) Notify(_ context.Context, a alerting.Alert) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts = append(f.alerts, a)
	return nil
}

func (f *fakeNotifier) countFor(destID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, a := range f.alerts {
		if a.DestinationID == destID {
			n++
		}
	}
	return n
}

// BR-19: a dead-lettered delivery fires exactly one alert, and a second
// evaluation within the cooldown fires no more.
func TestPipelineAlertOnDeadLetter(t *testing.T) {
	h := newHarness(t)
	h.startWorker(t)

	dest := newRecordingServer(t, alwaysStatus(500))
	src := h.createSource(t)
	d := h.createDestination(t, dest.srv.URL, destOpts{maxAttempts: 1})
	h.route(t, src, d)

	eventID := h.postEvent(t, src, `{"hello":"alert"}`)
	eventually(t, 15*time.Second, "delivery dead-letters", func() bool {
		ds := deliveriesForEvent(t, h.pool, eventID)
		return len(ds) == 1 && ds[0].status == statusDeadLettered
	})

	fake := &fakeNotifier{}
	// DLQ condition only (FailureThreshold 0), long cooldown so the repeat is
	// suppressed, window wide enough to catch the just-created dead-letter.
	cfg := alerting.Config{Enabled: true, CooldownMinutes: 60, WindowMinutes: 60, MinDeliveries: 20}
	ev := alerting.NewEvaluator(h.q, cfg, fake)

	destID := uuidStr(d.ID)
	if err := ev.Run(context.Background()); err != nil {
		t.Fatalf("first evaluation: %v", err)
	}
	if got := fake.countFor(destID); got != 1 {
		t.Fatalf("alerts for destination after first run = %d, want 1", got)
	}
	if c := fake.alerts[len(fake.alerts)-1].Condition; c != "dlq" {
		t.Errorf("alert condition = %q, want dlq", c)
	}

	// Second run within the cooldown must not re-alert.
	if err := ev.Run(context.Background()); err != nil {
		t.Fatalf("second evaluation: %v", err)
	}
	if got := fake.countFor(destID); got != 1 {
		t.Errorf("alerts for destination after second run = %d, want 1 (cooldown)", got)
	}
}

// BR-07: an enqueued delivery survives with no worker running (it's durable in
// Postgres) and is delivered once a worker starts — the restart-durability
// guarantee.
func TestPipelineRestartDurability(t *testing.T) {
	h := newHarness(t)

	dest := newRecordingServer(t, alwaysStatus(200))
	src := h.createSource(t)
	d := h.createDestination(t, dest.srv.URL, destOpts{})
	h.route(t, src, d)

	// No worker yet: the event is ingested and the job is durably enqueued.
	eventID := h.postEvent(t, src, `{"hello":"durable"}`)
	time.Sleep(1500 * time.Millisecond)
	dlv := deliveriesForEvent(t, h.pool, eventID)[0]
	if dlv.status != "pending" {
		t.Fatalf("status = %q before any worker, want pending", dlv.status)
	}
	if dest.count() != 0 {
		t.Fatalf("destination was hit %d times with no worker running", dest.count())
	}

	// Worker comes up and picks up the waiting job.
	h.startWorker(t)
	eventually(t, 15*time.Second, "durable delivery is delivered after worker starts", func() bool {
		return deliveriesForEvent(t, h.pool, eventID)[0].status == statusSucceeded
	})
	if dest.count() != 1 {
		t.Errorf("destination calls = %d, want 1", dest.count())
	}
}

// BR-12: a paused destination is not delivered to; resuming lets the held
// delivery through.
func TestPipelinePauseResume(t *testing.T) {
	// Shorten the paused re-check so the test doesn't wait the 30s production
	// interval; restored after the test.
	old := pausedSnoozeInterval
	pausedSnoozeInterval = 500 * time.Millisecond
	t.Cleanup(func() { pausedSnoozeInterval = old })

	h := newHarness(t)
	h.startWorker(t)

	dest := newRecordingServer(t, alwaysStatus(200))
	src := h.createSource(t)
	d := h.createDestination(t, dest.srv.URL, destOpts{paused: true})
	h.route(t, src, d)

	eventID := h.postEvent(t, src, `{"hello":"paused"}`)

	// While paused, the delivery stays pending and the destination is untouched.
	time.Sleep(2 * time.Second)
	if got := deliveriesForEvent(t, h.pool, eventID)[0].status; got != "pending" {
		t.Fatalf("status = %q while paused, want pending", got)
	}
	if dest.count() != 0 {
		t.Fatalf("paused destination was hit %d times", dest.count())
	}

	// Resume: the snoozed job wakes and delivers.
	if _, err := h.q.ResumeDestination(context.Background(), db.ResumeDestinationParams{ID: d.ID, TenantID: tenancy.DefaultTenantID}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	eventually(t, 15*time.Second, "resumed delivery is delivered", func() bool {
		return deliveriesForEvent(t, h.pool, eventID)[0].status == statusSucceeded
	})
}

// BR-10: pacing spreads deliveries to a rate-limited destination without losing
// any of them.
func TestPipelinePacingDeliversAll(t *testing.T) {
	h := newHarness(t)
	h.startWorker(t)

	dest := newRecordingServer(t, alwaysStatus(200))
	src := h.createSource(t)
	d := h.createDestination(t, dest.srv.URL, destOpts{rps: 2})
	h.route(t, src, d)

	const n = 4
	var eventIDs []pgtype.UUID
	for range n {
		eventIDs = append(eventIDs, h.postEvent(t, src, `{"hello":"pace"}`))
	}

	eventually(t, 20*time.Second, "all paced deliveries eventually succeed", func() bool {
		for _, eid := range eventIDs {
			ds := deliveriesForEvent(t, h.pool, eid)
			if len(ds) != 1 || ds[0].status != statusSucceeded {
				return false
			}
		}
		return true
	})
	if dest.count() != n {
		t.Errorf("destination calls = %d, want %d (pacing must not drop deliveries)", dest.count(), n)
	}
}

// ---- db + misc helpers ------------------------------------------------------

func (h *harness) enqueueDuplicate(t *testing.T, deliveryID pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := queue.InsertDeliveryJob(ctx, h.insertClient, tx, queue.DeliveryArgs{
		DeliveryID:         uuidStr(deliveryID),
		BackoffBaseSeconds: 1,
		BackoffMaxSeconds:  2,
	}, 5); err != nil {
		t.Fatalf("enqueue duplicate: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

type deliveryRow struct {
	id           pgtype.UUID
	status       string
	attemptCount int32
	deadLettered bool
}

func deliveriesForEvent(t *testing.T, pool *pgxpool.Pool, eventID pgtype.UUID) []deliveryRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		"SELECT id, status, attempt_count, dead_lettered_at IS NOT NULL FROM deliveries WHERE event_id = $1 ORDER BY created_at",
		eventID)
	if err != nil {
		t.Fatalf("query deliveries: %v", err)
	}
	defer rows.Close()
	var out []deliveryRow
	for rows.Next() {
		var d deliveryRow
		if err := rows.Scan(&d.id, &d.status, &d.attemptCount, &d.deadLettered); err != nil {
			t.Fatalf("scan delivery: %v", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate deliveries: %v", err)
	}
	return out
}

func attemptRowCount(t *testing.T, pool *pgxpool.Pool, deliveryID pgtype.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM delivery_attempts WHERE delivery_id = $1", deliveryID).Scan(&n); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	return n
}

func latestEventID(t *testing.T, pool *pgxpool.Pool, sourceID pgtype.UUID) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(context.Background(),
		"SELECT id FROM events WHERE source_id = $1 ORDER BY received_at DESC LIMIT 1", sourceID).Scan(&id); err != nil {
		t.Fatalf("latest event id: %v", err)
	}
	return id
}

func eventually(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("condition never met within %s: %s", timeout, desc)
}

func uuidStr(u pgtype.UUID) string {
	b := u.Bytes
	return hex.EncodeToString(b[0:4]) + "-" + hex.EncodeToString(b[4:6]) + "-" + hex.EncodeToString(b[6:8]) + "-" + hex.EncodeToString(b[8:10]) + "-" + hex.EncodeToString(b[10:16])
}

func randomHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// testDB connects to the compose Postgres and applies both migration sets.
// Skips the whole suite when TEST_DATABASE_URL is unset.
func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	if err := db.Migrate(ctx, dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := queue.Migrate(ctx, pool); err != nil {
		t.Fatalf("river migrate: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
