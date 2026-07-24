package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"webhook-gateway/internal/api/middleware"
	"webhook-gateway/internal/crypto"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/ingest"
	"webhook-gateway/internal/queue"
	"webhook-gateway/internal/sourcedef"
	"webhook-gateway/internal/tenancy"
)

// testEncryptionKey is a fixed 32-byte AES-256 key, base64-encoded — test-only.
const testEncryptionKey = "ZGV2LTMyLWJ5dGUtZW5jcnlwdGlvbi1rZXktMDAwMDA="

// TestSourcesAPIIntegration drives the sources API against the compose Postgres.
// Like the ingest integration test, the whole test is skipped unless
// TEST_DATABASE_URL points at a running database:
//
//	make db-up
//	TEST_DATABASE_URL='postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable' \
//	    go test ./internal/api -v
func TestSourcesAPIIntegration(t *testing.T) {
	pool := testDB(t)
	enc := testEncryptor(t)
	catalog := testCatalog(t)
	q := db.New(pool)

	const adminPassword = "test-admin-password"
	mux := http.NewServeMux()
	RegisterSources(mux, q, enc, catalog, middleware.NewAuth(q, adminPassword), testIngestHandler(t, pool, q, enc, catalog))

	const secret = "whsec_super_secret_value"

	// --- create ---
	createBody := `{"name":"integration-test-src","provider_type":"stripe","signing_secret":"` + secret + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/sources", strings.NewReader(createBody))
	req.Header.Set("Authorization", "Bearer "+adminPassword)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var created sourceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM sources WHERE endpoint_path = $1", created.EndpointPath)
	})

	if !strings.HasPrefix(created.EndpointPath, "src_") {
		t.Errorf("endpoint_path %q missing src_ prefix", created.EndpointPath)
	}

	// --- secret is encrypted at rest (BR-26) ---
	var encrypted []byte
	if err := pool.QueryRow(context.Background(),
		"SELECT signing_secret_encrypted FROM sources WHERE endpoint_path = $1", created.EndpointPath,
	).Scan(&encrypted); err != nil {
		t.Fatalf("reading stored secret: %v", err)
	}
	if len(encrypted) == 0 {
		t.Fatal("signing_secret_encrypted is empty; secret was not stored")
	}
	if bytes.Contains(encrypted, []byte(secret)) {
		t.Error("plaintext secret found in signing_secret_encrypted column")
	}
	if dec, err := enc.Decrypt(encrypted); err != nil {
		t.Errorf("stored secret does not decrypt: %v", err)
	} else if string(dec) != secret {
		t.Errorf("decrypted secret = %q, want %q", dec, secret)
	}

	// --- list returns the created source ---
	listReq := httptest.NewRequest(http.MethodGet, "/api/sources", nil)
	listReq.Header.Set("Authorization", "Bearer "+adminPassword)
	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body=%s", listRec.Code, listRec.Body.String())
	}
	var listed []sourceResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decoding list response: %v", err)
	}
	found := false
	for _, s := range listed {
		if s.EndpointPath == created.EndpointPath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("created source %q not present in list", created.EndpointPath)
	}

	// --- list without the admin bearer is rejected ---
	noAuthRec := httptest.NewRecorder()
	mux.ServeHTTP(noAuthRec, httptest.NewRequest(http.MethodGet, "/api/sources", nil))
	if noAuthRec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated list status = %d, want 401", noAuthRec.Code)
	}
}

// TestSourceTestEventAPIIntegration covers #25's done-criteria: per catalog
// provider, a generated test event is signed with the source's real secret,
// verified through the real ingest path, and produces a delivery via the
// normal route.
func TestSourceTestEventAPIIntegration(t *testing.T) {
	pool := testDB(t)
	enc := testEncryptor(t)
	catalog := testCatalog(t)
	q := db.New(pool)

	const adminPassword = "test-admin-password"
	mux := http.NewServeMux()
	RegisterSources(mux, q, enc, catalog, middleware.NewAuth(q, adminPassword), testIngestHandler(t, pool, q, enc, catalog))

	authed := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+adminPassword)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	for _, provider := range []string{"stripe", "github", "generic_hmac"} {
		t.Run(provider, func(t *testing.T) {
			createBody := `{"name":"test-event-` + provider + `","provider_type":"` + provider + `","signing_secret":"test-secret-value"}`
			rec := authed(http.MethodPost, "/api/sources", createBody)
			if rec.Code != http.StatusCreated {
				t.Fatalf("create source status = %d, want 201; body=%s", rec.Code, rec.Body.String())
			}
			var src sourceResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &src); err != nil {
				t.Fatalf("decoding create response: %v", err)
			}
			srcID, err := parseUUID(src.ID)
			if err != nil {
				t.Fatalf("parsing source id: %v", err)
			}
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), "DELETE FROM sources WHERE id = $1", srcID)
			})

			dest, err := q.InsertDestination(context.Background(), db.InsertDestinationParams{
				TenantID:           tenancy.DefaultTenantID,
				Name:               "test-event-dest-" + provider,
				Url:                "http://localhost:0/nowhere",
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

			if _, err := q.InsertRoute(context.Background(), db.InsertRouteParams{
				TenantID:      tenancy.DefaultTenantID,
				SourceID:      srcID,
				DestinationID: dest.ID,
				Enabled:       true,
			}); err != nil {
				t.Fatalf("insert route: %v", err)
			}

			teRec := authed(http.MethodPost, "/api/sources/"+src.ID+"/test-event", "")
			if teRec.Code != http.StatusOK {
				t.Fatalf("test-event status = %d, want 200; body=%s", teRec.Code, teRec.Body.String())
			}
			var got testEventResponse
			if err := json.Unmarshal(teRec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decoding test-event response: %v", err)
			}
			if !got.Verified {
				t.Errorf("provider %q: test event did not verify", provider)
			}
			eventID, err := parseUUID(got.EventID)
			if err != nil {
				t.Fatalf("parsing event id: %v", err)
			}
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), "DELETE FROM events WHERE id = $1", eventID)
			})

			var deliveryCount int
			if err := pool.QueryRow(context.Background(),
				"SELECT count(*) FROM deliveries WHERE event_id = $1", eventID,
			).Scan(&deliveryCount); err != nil {
				t.Fatalf("counting deliveries: %v", err)
			}
			if deliveryCount != 1 {
				t.Errorf("provider %q: deliveries for test event = %d, want 1", provider, deliveryCount)
			}
		})
	}
}

// --- helpers ---

// testIngestHandler builds an ingest.Handler wired the same way main.go
// wires it, without mounting the real /ingest/{path} route — the sources API
// test-event endpoint (#25) only needs the handler's ProcessEvent method.
func testIngestHandler(t *testing.T, pool *pgxpool.Pool, q *db.Queries, enc *crypto.Encryptor, catalog map[string]sourcedef.Definition) *ingest.Handler {
	t.Helper()
	insertClient, err := queue.NewInsertOnlyClient(pool)
	if err != nil {
		t.Fatalf("insert client: %v", err)
	}
	return ingest.Register(http.NewServeMux(), pool, q, insertClient, enc, catalog, ingest.Options{
		MaxBodyBytes:       1 << 20,
		RateLimitPerSecond: 1000,
	})
}

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
