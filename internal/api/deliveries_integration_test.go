package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"webhook-gateway/internal/db"
	"webhook-gateway/internal/queue"
	"webhook-gateway/internal/tenancy"
)

func TestDeliveriesRecoverAPIIntegration(t *testing.T) {
	pool := testDB(t)
	q := db.New(pool)

	insertClient, err := queue.NewInsertOnlyClient(pool)
	if err != nil {
		t.Fatalf("insert client: %v", err)
	}

	const adminPassword = "test-admin-password"
	mux := http.NewServeMux()
	RegisterDeliveries(mux, pool, q, insertClient, adminPassword)

	recover := func(id, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/deliveries/"+id+"/recover", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	someUUID := "00000000-0000-7000-8000-0000000000aa"

	t.Run("no auth is rejected", func(t *testing.T) {
		if rec := recover(someUUID, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("wrong password is rejected", func(t *testing.T) {
		if rec := recover(someUUID, "not-the-password"); rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("invalid id is a bad request", func(t *testing.T) {
		if rec := recover("not-a-uuid", adminPassword); rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("unknown delivery is not found", func(t *testing.T) {
		if rec := recover(someUUID, adminPassword); rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("non-dead-lettered delivery is a conflict", func(t *testing.T) {
		id := createPendingDelivery(t, pool, q)
		if rec := recover(uuidString(id), adminPassword); rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 (delivery is still pending); body=%s", rec.Code, rec.Body.String())
		}
	})
}

func createPendingDelivery(t *testing.T, pool *pgxpool.Pool, q *db.Queries) pgtype.UUID {
	t.Helper()
	ctx := context.Background()

	src, err := q.InsertSource(ctx, db.InsertSourceParams{
		TenantID:           tenancy.DefaultTenantID,
		Name:               "recover-test-source",
		ProviderType:       "none",
		EndpointPath:       "src_" + randHex(t),
		VerificationConfig: []byte("{}"),
	})
	if err != nil {
		t.Fatalf("insert source: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM sources WHERE id = $1", src.ID) })

	event, err := q.InsertEvent(ctx, db.InsertEventParams{
		TenantID:   tenancy.DefaultTenantID,
		SourceID:   src.ID,
		RawHeaders: []byte("{}"),
		RawBody:    []byte("{}"),
		Verified:   true,
	})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}

	dest, err := q.InsertDestination(ctx, db.InsertDestinationParams{
		TenantID:           tenancy.DefaultTenantID,
		Name:               "recover-test-dest",
		Url:                "https://example.test/hook",
		AuthConfig:         []byte("{}"),
		TimeoutMs:          2000,
		MaxAttempts:        5,
		BackoffBaseSeconds: 1,
		BackoffMaxSeconds:  2,
	})
	if err != nil {
		t.Fatalf("insert destination: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM destinations WHERE id = $1", dest.ID) })

	deliveryID, err := q.InsertDelivery(ctx, db.InsertDeliveryParams{
		TenantID:      tenancy.DefaultTenantID,
		EventID:       event.ID,
		DestinationID: dest.ID,
	})
	if err != nil {
		t.Fatalf("insert delivery: %v", err)
	}
	return deliveryID
}

func randHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}
