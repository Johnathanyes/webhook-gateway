package ingest_test

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"webhook-gateway/internal/crypto"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/ingest"
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
	ingest.Register(mux, pool, q, enc, catalog, generousOpts)

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

// TestIngestMaxBodySize covers the payload cap: an oversized body is rejected
// with 413 and nothing is persisted (BR-06).
func TestIngestMaxBodySize(t *testing.T) {
	pool := testDB(t)
	enc := testEncryptor(t)
	catalog := testCatalog(t)
	q := db.New(pool)

	source := createGithubSource(t, pool, enc, []byte("secret"))

	mux := http.NewServeMux()
	ingest.Register(mux, pool, q, enc, catalog, ingest.Options{MaxBodyBytes: 16, RateLimitPerSecond: 1000})

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
	ingest.Register(mux, pool, q, enc, catalog, ingest.Options{MaxBodyBytes: 1 << 20, RateLimitPerSecond: 1})

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

// createGithubSource inserts a github source with an encrypted secret and
// registers cleanup of it and its events.
func createGithubSource(t *testing.T, pool *pgxpool.Pool, enc *crypto.Encryptor, secret []byte) db.Source {
	t.Helper()
	ctx := context.Background()
	encrypted, version, err := enc.Encrypt(secret)
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	source, err := db.New(pool).InsertSource(ctx, db.InsertSourceParams{
		TenantID:                tenancy.DefaultTenantID,
		Name:                    "ingest-test",
		ProviderType:            "github",
		EndpointPath:            "src_" + randomHex(t),
		SigningSecretEncrypted:  encrypted,
		SigningSecretKeyVersion: pgtype.Int4{Int32: int32(version), Valid: true},
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

func randomHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}
