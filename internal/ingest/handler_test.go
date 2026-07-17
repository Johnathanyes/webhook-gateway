package ingest_test

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"webhook-gateway/internal/crypto"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/ingest"
	"webhook-gateway/internal/queue"
	"webhook-gateway/internal/sourcedef"
	"webhook-gateway/internal/tenancy"
)

// testEncryptionKey is a fixed 32-byte AES-256 key, base64-encoded — test-only.
const testEncryptionKey = "ZGV2LTMyLWJ5dGUtZW5jcnlwdGlvbi1rZXktMDAwMDA="

// generousOpts disables the abuse limits so tests that aren't about them see
// plain behavior.
var generousOpts = ingest.Options{MaxBodyBytes: 1 << 20, RateLimitPerSecond: 1000}

// TestIngestStoresEvent drives the real handler against the compose Postgres.
// The whole file is skipped unless TEST_DATABASE_URL points at a running
// database (so the CI `go test ./...` with no Postgres stays green):
//
//	make db-up
//	TEST_DATABASE_URL='postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable' \
//	    go test ./internal/ingest -v
func TestIngestStoresEvent(t *testing.T) {
	pool := testDB(t)
	enc := testEncryptor(t)
	catalog := testCatalog(t)
	q := db.New(pool)

	secret := []byte("test-signing-secret")
	source := createGithubSource(t, pool, enc, secret)

	mux := http.NewServeMux()
	ingest.Register(mux, pool, q, testRiverClient(t, pool), enc, catalog, generousOpts)

	body := []byte(`{"action":"opened","number":42}`)

	t.Run("signed request stores verified event with identical body", func(t *testing.T) {
		rec := post(mux, source.EndpointPath, body, map[string]string{
			"Content-Type":        "application/json",
			"X-Hub-Signature-256": githubSignature(secret, body),
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}

		storedBody, verified := latestEvent(t, pool, source.ID)
		if string(storedBody) != string(body) {
			t.Errorf("stored raw_body = %q, want %q", storedBody, body)
		}
		if !verified {
			t.Errorf("verified = false, want true for a correctly signed request")
		}
	})

	t.Run("tampered signature stores unverified event", func(t *testing.T) {
		rec := post(mux, source.EndpointPath, body, map[string]string{
			"Content-Type":        "application/json",
			"X-Hub-Signature-256": githubSignature([]byte("wrong-secret"), body),
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (tampered events are still stored); body=%s", rec.Code, rec.Body.String())
		}

		_, verified := latestEvent(t, pool, source.ID)
		if verified {
			t.Errorf("verified = true, want false for a tampered signature")
		}
	})

	t.Run("unknown path returns 404", func(t *testing.T) {
		rec := post(mux, "src_does_not_exist", []byte("{}"), nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

// TestIngestTransactionalEnqueue covers BR-07: a stored event fans out to a
// pending delivery + enqueued River job per enabled route, committed in the
// same transaction as the event. Disabled routes and unrouted sources produce
// no deliveries.
func TestIngestTransactionalEnqueue(t *testing.T) {
	pool := testDB(t)
	enc := testEncryptor(t)
	catalog := testCatalog(t)
	q := db.New(pool)

	secret := []byte("test-signing-secret")
	source := createGithubSource(t, pool, enc, secret)

	mux := http.NewServeMux()
	ingest.Register(mux, pool, q, testRiverClient(t, pool), enc, catalog, generousOpts)

	body := []byte(`{"action":"opened"}`)
	sign := func() map[string]string {
		return map[string]string{
			"Content-Type":        "application/json",
			"X-Hub-Signature-256": githubSignature(secret, body),
		}
	}

	t.Run("no routes means no deliveries", func(t *testing.T) {
		if rec := post(mux, source.EndpointPath, body, sign()); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if n := deliveryCount(t, pool, source.ID); n != 0 {
			t.Errorf("deliveries = %d, want 0 for an unrouted source", n)
		}
	})

	dest := createDestination(t, pool)
	createRoute(t, pool, source.ID, dest.ID, true)
	disabledDest := createDestination(t, pool)
	createRoute(t, pool, source.ID, disabledDest.ID, false)

	t.Run("one enabled route yields one delivery with a river job", func(t *testing.T) {
		if rec := post(mux, source.EndpointPath, body, sign()); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}

		event := latestEventID(t, pool, source.ID)
		deliveries := deliveriesForEvent(t, pool, event)
		if len(deliveries) != 1 {
			t.Fatalf("deliveries for event = %d, want 1 (enabled route only)", len(deliveries))
		}
		d := deliveries[0]
		if d.destinationID != uuidStr(dest.ID) {
			t.Errorf("delivery destination = %s, want %s", d.destinationID, uuidStr(dest.ID))
		}
		if d.status != "pending" {
			t.Errorf("delivery status = %q, want pending", d.status)
		}
		if !d.riverJobID.Valid {
			t.Errorf("river_job_id is NULL, want it backfilled from the enqueued job")
		}
		// The job must be committed in river_job, not just referenced.
		if !riverJobExists(t, pool, d.riverJobID.Int64) {
			t.Errorf("river_job %d not found; enqueue did not commit with the event", d.riverJobID.Int64)
		}
	})
}

// TestIngestMaxBodySize covers the payload cap: an oversized body is rejected
// with 413 and nothing is persisted (BR-06).
func TestIngestMaxBodySize(t *testing.T) {
	pool := testDB(t)
	enc := testEncryptor(t)
	catalog := testCatalog(t)
	q := db.New(pool)

	source := createGithubSource(t, pool, enc, []byte("secret"))

	mux := http.NewServeMux()
	ingest.Register(mux, pool, q, testRiverClient(t, pool), enc, catalog, ingest.Options{MaxBodyBytes: 16, RateLimitPerSecond: 1000})

	rec := post(mux, source.EndpointPath, []byte(strings.Repeat("x", 1024)), nil)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", rec.Code, rec.Body.String())
	}
	if n := eventCount(t, pool, source.ID); n != 0 {
		t.Errorf("events persisted = %d, want 0 for an oversized request", n)
	}
}

// TestIngestRateLimit covers the per-source token bucket: past the burst,
// requests get 429 (BR-06). Rate 1 means burst 1, so the second immediate
// request has no tokens left.
func TestIngestRateLimit(t *testing.T) {
	pool := testDB(t)
	enc := testEncryptor(t)
	catalog := testCatalog(t)
	q := db.New(pool)

	source := createGithubSource(t, pool, enc, []byte("secret"))

	mux := http.NewServeMux()
	ingest.Register(mux, pool, q, testRiverClient(t, pool), enc, catalog, ingest.Options{MaxBodyBytes: 1 << 20, RateLimitPerSecond: 1})

	// First request spends the single token.
	if rec := post(mux, source.EndpointPath, []byte("{}"), nil); rec.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Immediate follow-ups find the bucket empty.
	got429 := false
	for i := 0; i < 3; i++ {
		if rec := post(mux, source.EndpointPath, []byte("{}"), nil); rec.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Errorf("expected a 429 after exhausting the burst, got none")
	}
}

// TestIngestNoneProvider covers a source whose provider_type is "none": it has
// no catalog verifier, so every event is stored verified=true regardless of
// headers.
func TestIngestNoneProvider(t *testing.T) {
	pool := testDB(t)
	enc := testEncryptor(t)
	catalog := testCatalog(t)
	q := db.New(pool)

	source := createSource(t, pool, enc, "none", nil)

	mux := http.NewServeMux()
	ingest.Register(mux, pool, q, testRiverClient(t, pool), enc, catalog, generousOpts)

	body := []byte(`{"anything":"goes"}`)
	rec := post(mux, source.EndpointPath, body, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	storedBody, verified := latestEvent(t, pool, source.ID)
	if string(storedBody) != string(body) {
		t.Errorf("stored raw_body = %q, want %q", storedBody, body)
	}
	if !verified {
		t.Error("verified = false, want true for a provider-less source")
	}
}

// TestIngestStripeSigned drives the full pipeline for the Stripe verifier (the
// timestamped scheme, distinct from GitHub's prefix scheme): a fresh signature
// stores verified=true, an expired one still stores but verified=false.
func TestIngestStripeSigned(t *testing.T) {
	pool := testDB(t)
	enc := testEncryptor(t)
	catalog := testCatalog(t)
	q := db.New(pool)

	secret := []byte("whsec_test_secret")
	source := createSource(t, pool, enc, "stripe", secret)

	mux := http.NewServeMux()
	ingest.Register(mux, pool, q, testRiverClient(t, pool), enc, catalog, generousOpts)

	body := []byte(`{"id":"evt_1","object":"event"}`)

	t.Run("fresh signature verifies", func(t *testing.T) {
		rec := post(mux, source.EndpointPath, body, map[string]string{
			"Content-Type":     "application/json",
			"Stripe-Signature": stripeSignature(secret, body, time.Now().Unix()),
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if _, verified := latestEvent(t, pool, source.ID); !verified {
			t.Error("verified = false, want true for a fresh Stripe signature")
		}
	})

	t.Run("expired signature stores unverified", func(t *testing.T) {
		rec := post(mux, source.EndpointPath, body, map[string]string{
			"Content-Type":     "application/json",
			"Stripe-Signature": stripeSignature(secret, body, time.Now().Unix()-3600),
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if _, verified := latestEvent(t, pool, source.ID); verified {
			t.Error("verified = true, want false for an expired Stripe timestamp")
		}
	})
}

// TestIngestDecryptFailure covers a source whose stored secret can't be
// decrypted with the running key (e.g. a rotated ENCRYPTION_KEY): the event is
// still stored with verified=false and a 200, so the misconfiguration shows up
// in the log instead of dropping webhooks.
func TestIngestDecryptFailure(t *testing.T) {
	pool := testDB(t)
	enc := testEncryptor(t)
	catalog := testCatalog(t)
	q := db.New(pool)

	// The source's secret is encrypted under a different key than the handler
	// holds, so the handler's Decrypt fails.
	otherKey := base64.StdEncoding.EncodeToString([]byte("wrong-32-byte-encryption-key-000"))
	otherEnc, err := crypto.NewEncryptor(otherKey)
	if err != nil {
		t.Fatalf("building second encryptor: %v", err)
	}
	secret := []byte("test-signing-secret")
	source := createSource(t, pool, otherEnc, "github", secret)

	mux := http.NewServeMux()
	ingest.Register(mux, pool, q, testRiverClient(t, pool), enc, catalog, generousOpts)

	body := []byte(`{"action":"opened"}`)
	rec := post(mux, source.EndpointPath, body, map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": githubSignature(secret, body),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (undecryptable secret still stores); body=%s", rec.Code, rec.Body.String())
	}

	storedBody, verified := latestEvent(t, pool, source.ID)
	if string(storedBody) != string(body) {
		t.Errorf("stored raw_body = %q, want %q", storedBody, body)
	}
	if verified {
		t.Error("verified = true, want false when the signing secret can't be decrypted")
	}
}

// TestIngestNonJSONBody covers a non-JSON payload: raw_body is stored
// byte-identical (including NUL bytes) and parsed_body stays NULL.
func TestIngestNonJSONBody(t *testing.T) {
	pool := testDB(t)
	enc := testEncryptor(t)
	catalog := testCatalog(t)
	q := db.New(pool)

	source := createGithubSource(t, pool, enc, []byte("secret"))

	mux := http.NewServeMux()
	ingest.Register(mux, pool, q, testRiverClient(t, pool), enc, catalog, generousOpts)

	body := []byte{0x00, 0x01, 0x02, 0xff, 'n', 'o', 't', 0x00, 'j', 's', 'o', 'n'}
	rec := post(mux, source.EndpointPath, body, map[string]string{"Content-Type": "application/octet-stream"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	rawBody, parsedBody, contentType, _ := latestEventFull(t, pool, source.ID)
	if string(rawBody) != string(body) {
		t.Errorf("stored raw_body = %v, want %v", rawBody, body)
	}
	if parsedBody != nil {
		t.Errorf("parsed_body = %q, want NULL for a non-JSON body", parsedBody)
	}
	if contentType != "application/octet-stream" {
		t.Errorf("content_type = %q, want application/octet-stream", contentType)
	}
}

// TestIngestMultiValueHeaders confirms a repeated request header survives the
// raw_headers JSONB round-trip with both values intact.
func TestIngestMultiValueHeaders(t *testing.T) {
	pool := testDB(t)
	enc := testEncryptor(t)
	catalog := testCatalog(t)
	q := db.New(pool)

	source := createGithubSource(t, pool, enc, []byte("secret"))

	mux := http.NewServeMux()
	ingest.Register(mux, pool, q, testRiverClient(t, pool), enc, catalog, generousOpts)

	req := httptest.NewRequest(http.MethodPost, "/ingest/"+source.EndpointPath, strings.NewReader("{}"))
	req.Header.Add("X-Custom", "one")
	req.Header.Add("X-Custom", "two")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	_, _, _, rawHeaders := latestEventFull(t, pool, source.ID)
	var headers map[string][]string
	if err := json.Unmarshal(rawHeaders, &headers); err != nil {
		t.Fatalf("unmarshaling raw_headers: %v", err)
	}
	if got := headers["X-Custom"]; len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("X-Custom = %v, want [one two]", got)
	}
}

// TestIngestConcurrent fires many signed requests at one source in parallel:
// all get 200 and every event is persisted. Run with -race, it also guards the
// handler and limiter against data races.
func TestIngestConcurrent(t *testing.T) {
	pool := testDB(t)
	enc := testEncryptor(t)
	catalog := testCatalog(t)
	q := db.New(pool)

	secret := []byte("secret")
	source := createGithubSource(t, pool, enc, secret)

	mux := http.NewServeMux()
	ingest.Register(mux, pool, q, testRiverClient(t, pool), enc, catalog, generousOpts)

	const n = 20
	body := []byte(`{"action":"opened"}`)
	sig := githubSignature(secret, body)

	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := post(mux, source.EndpointPath, body, map[string]string{
				"Content-Type":        "application/json",
				"X-Hub-Signature-256": sig,
			})
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("request %d status = %d, want 200", i, code)
		}
	}
	if got := eventCount(t, pool, source.ID); got != n {
		t.Errorf("stored events = %d, want %d", got, n)
	}
}

// --- helpers ---

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

// testRiverClient runs River's migrations nd returns an insert-only client, mirroring the
// ingest role's wiring in main.go.
func testRiverClient(t *testing.T, pool *pgxpool.Pool) *river.Client[pgx.Tx] {
	t.Helper()
	if err := queue.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("river migrate: %v", err)
	}
	client, err := queue.NewInsertOnlyClient(pool)
	if err != nil {
		t.Fatalf("river client: %v", err)
	}
	return client
}

func testEncryptor(t *testing.T) *crypto.Encryptor {
	t.Helper()
	enc, err := crypto.NewEncryptor(testEncryptionKey)
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	return enc
}

func testCatalog(t *testing.T) map[string]sourcedef.Definition {
	t.Helper()
	catalog, err := sourcedef.Load()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return catalog
}

// createGithubSource inserts a github source with an encrypted secret. It is a
// thin wrapper over createSource for the common case.
func createGithubSource(t *testing.T, pool *pgxpool.Pool, enc *crypto.Encryptor, secret []byte) db.Source {
	t.Helper()
	return createSource(t, pool, enc, "github", secret)
}

// createSource inserts a source for the given provider, encrypting secret with
// enc (an empty secret leaves the secret columns NULL, as provider "none"
// needs), and registers cleanup of it and its events. Passing an encryptor
// keyed differently from the handler's lets a test exercise the decrypt-failure
// path.
func createSource(t *testing.T, pool *pgxpool.Pool, enc *crypto.Encryptor, provider string, secret []byte) db.Source {
	t.Helper()
	ctx := context.Background()

	var encrypted []byte
	var version pgtype.Int4
	if len(secret) > 0 {
		ciphertext, v, err := enc.Encrypt(secret)
		if err != nil {
			t.Fatalf("encrypt secret: %v", err)
		}
		encrypted = ciphertext
		version = pgtype.Int4{Int32: int32(v), Valid: true}
	}

	source, err := db.New(pool).InsertSource(ctx, db.InsertSourceParams{
		TenantID:                tenancy.DefaultTenantID,
		Name:                    "ingest-test",
		ProviderType:            provider,
		EndpointPath:            "src_" + randomHex(t),
		SigningSecretEncrypted:  encrypted,
		SigningSecretKeyVersion: version,
		VerificationConfig:      []byte("{}"),
	})
	if err != nil {
		t.Fatalf("insert source: %v", err)
	}
	t.Cleanup(func() {
		// Events FK to the source, so clear them first.
		_, _ = pool.Exec(ctx, "DELETE FROM events WHERE source_id = $1", source.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM sources WHERE id = $1", source.ID)
	})
	return source
}

func post(mux *http.ServeMux, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/ingest/"+path, strings.NewReader(string(body)))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func githubSignature(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// stripeSignature builds a Stripe-Signature header value for body signed at ts:
// HMAC-SHA256 over "<ts>.<body>", hex, in the t/v1 field format.
func stripeSignature(secret, body []byte, ts int64) string {
	payload := strconv.FormatInt(ts, 10) + "." + string(body)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

// latestEvent returns the raw_body and verified flag of the most recent event
// for a source.
func latestEvent(t *testing.T, pool *pgxpool.Pool, sourceID pgtype.UUID) ([]byte, bool) {
	t.Helper()
	var rawBody []byte
	var verified bool
	err := pool.QueryRow(context.Background(),
		"SELECT raw_body, verified FROM events WHERE source_id = $1 ORDER BY received_at DESC LIMIT 1",
		sourceID,
	).Scan(&rawBody, &verified)
	if err != nil {
		t.Fatalf("querying stored event: %v", err)
	}
	return rawBody, verified
}

// latestEventFull returns the raw_body, parsed_body (nil when the column is
// NULL), content_type, and raw_headers of the most recent event for a source.
func latestEventFull(t *testing.T, pool *pgxpool.Pool, sourceID pgtype.UUID) (rawBody, parsedBody []byte, contentType string, rawHeaders []byte) {
	t.Helper()
	var ct *string
	err := pool.QueryRow(context.Background(),
		"SELECT raw_body, parsed_body, content_type, raw_headers FROM events WHERE source_id = $1 ORDER BY received_at DESC LIMIT 1",
		sourceID,
	).Scan(&rawBody, &parsedBody, &ct, &rawHeaders)
	if err != nil {
		t.Fatalf("querying stored event: %v", err)
	}
	if ct != nil {
		contentType = *ct
	}
	return rawBody, parsedBody, contentType, rawHeaders
}

func eventCount(t *testing.T, pool *pgxpool.Pool, sourceID pgtype.UUID) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM events WHERE source_id = $1", sourceID,
	).Scan(&n)
	if err != nil {
		t.Fatalf("counting events: %v", err)
	}
	return n
}

// createDestination inserts a minimal destination and registers cleanup
func createDestination(t *testing.T, pool *pgxpool.Pool) db.Destination {
	t.Helper()
	ctx := context.Background()
	dest, err := db.New(pool).InsertDestination(ctx, db.InsertDestinationParams{
		TenantID:           tenancy.DefaultTenantID,
		Name:               "ingest-test-dest",
		Url:                "https://example.test/hook",
		AuthConfig:         []byte("{}"),
		TimeoutMs:          5000,
		RateLimitPerSecond: pgtype.Int4{},
		MaxAttempts:        8,
		BackoffBaseSeconds: 1,
		BackoffMaxSeconds:  3600,
	})
	if err != nil {
		t.Fatalf("insert destination: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM destinations WHERE id = $1", dest.ID)
	})
	return dest
}

func createRoute(t *testing.T, pool *pgxpool.Pool, sourceID, destID pgtype.UUID, enabled bool) {
	t.Helper()
	_, err := db.New(pool).InsertRoute(context.Background(), db.InsertRouteParams{
		TenantID:      tenancy.DefaultTenantID,
		SourceID:      sourceID,
		DestinationID: destID,
		Enabled:       enabled,
	})
	if err != nil {
		t.Fatalf("insert route: %v", err)
	}
}

func latestEventID(t *testing.T, pool *pgxpool.Pool, sourceID pgtype.UUID) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	err := pool.QueryRow(context.Background(),
		"SELECT id FROM events WHERE source_id = $1 ORDER BY received_at DESC LIMIT 1",
		sourceID,
	).Scan(&id)
	if err != nil {
		t.Fatalf("querying latest event id: %v", err)
	}
	return id
}

type testDelivery struct {
	destinationID string
	status        string
	riverJobID    pgtype.Int8
}

func deliveriesForEvent(t *testing.T, pool *pgxpool.Pool, eventID pgtype.UUID) []testDelivery {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		"SELECT destination_id, status, river_job_id FROM deliveries WHERE event_id = $1",
		eventID,
	)
	if err != nil {
		t.Fatalf("querying deliveries: %v", err)
	}
	defer rows.Close()
	var out []testDelivery
	for rows.Next() {
		var (
			destID pgtype.UUID
			d      testDelivery
		)
		if err := rows.Scan(&destID, &d.status, &d.riverJobID); err != nil {
			t.Fatalf("scanning delivery: %v", err)
		}
		d.destinationID = uuidStr(destID)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating deliveries: %v", err)
	}
	return out
}

func deliveryCount(t *testing.T, pool *pgxpool.Pool, sourceID pgtype.UUID) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM deliveries d JOIN events e ON e.id = d.event_id WHERE e.source_id = $1",
		sourceID,
	).Scan(&n)
	if err != nil {
		t.Fatalf("counting deliveries: %v", err)
	}
	return n
}

func riverJobExists(t *testing.T, pool *pgxpool.Pool, jobID int64) bool {
	t.Helper()
	var exists bool
	err := pool.QueryRow(context.Background(),
		"SELECT EXISTS (SELECT 1 FROM river_job WHERE id = $1)", jobID,
	).Scan(&exists)
	if err != nil {
		t.Fatalf("checking river_job: %v", err)
	}
	return exists
}

func uuidStr(u pgtype.UUID) string {
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func randomHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}
