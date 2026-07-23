package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"webhook-gateway/internal/api/middleware"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/tenancy"
)

// TestEventsAPIIntegration drives the events list/search/get API (#18) against
// the compose Postgres. Skipped unless TEST_DATABASE_URL is set. Every query is
// scoped to freshly created sources so it stays isolated from other rows in the
// shared database.
func TestEventsAPIIntegration(t *testing.T) {
	pool := testDB(t)
	q := db.New(pool)
	ctx := context.Background()

	// A made-up alphabetic token so full-text search matches exactly one set of
	// events regardless of what else is in the DB.
	marker := "quantumflux" + randHex(t)

	srcA := insertEventsTestSource(t, pool, q)
	srcB := insertEventsTestSource(t, pool, q)

	insert := func(source pgtype.UUID, verified bool, body string) pgtype.UUID {
		t.Helper()
		row, err := q.InsertEvent(ctx, db.InsertEventParams{
			TenantID:    tenancy.DefaultTenantID,
			SourceID:    source,
			RawHeaders:  []byte(`{"X-Test":"header"}`),
			RawBody:     []byte(body),
			ContentType: pgtype.Text{String: "application/json", Valid: true},
			ParsedBody:  []byte(body),
			Verified:    verified,
		})
		if err != nil {
			t.Fatalf("insert event: %v", err)
		}
		return row.ID
	}

	// srcA: three events carrying the marker (one unverified); srcB: one event
	// without it. Inserted in this order, so ids sort e1 < e2 < e3.
	e1 := insert(srcA, true, `{"marker":"`+marker+`","kind":"payment"}`)
	e2 := insert(srcA, true, `{"marker":"`+marker+`","kind":"refund"}`)
	e3 := insert(srcA, false, `{"marker":"`+marker+`","kind":"chargeback"}`)
	insert(srcB, true, `{"kind":"unrelated"}`)

	const adminPassword = "test-admin-password"
	mux := http.NewServeMux()
	RegisterEvents(mux, q, middleware.NewAuth(q, adminPassword))

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+adminPassword)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	listOK := func(t *testing.T, path string) listEventsResponse {
		t.Helper()
		rec := get(path)
		if rec.Code != http.StatusOK {
			t.Fatalf("list %s status = %d, want 200; body=%s", path, rec.Code, rec.Body.String())
		}
		var out listEventsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decoding list response: %v", err)
		}
		return out
	}

	t.Run("no auth is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("search finds only marker events, newest first", func(t *testing.T) {
		out := listOK(t, "/api/events?search="+marker)
		if got := ids(out); !equalIDs(got, []string{uuidString(e3), uuidString(e2), uuidString(e1)}) {
			t.Errorf("search results = %v, want e3,e2,e1", got)
		}
	})

	t.Run("filter by source_id", func(t *testing.T) {
		out := listOK(t, "/api/events?source_id="+uuidString(srcB))
		if len(out.Events) != 1 {
			t.Fatalf("srcB events = %d, want 1", len(out.Events))
		}
	})

	t.Run("filter by verified=false", func(t *testing.T) {
		out := listOK(t, "/api/events?source_id="+uuidString(srcA)+"&verified=false")
		if got := ids(out); !equalIDs(got, []string{uuidString(e3)}) {
			t.Errorf("verified=false results = %v, want just e3", got)
		}
	})

	t.Run("bad filter is a 400", func(t *testing.T) {
		if rec := get("/api/events?verified=maybe"); rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
		if rec := get("/api/events?delivery_status=bogus"); rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("pagination is stable under inserts", func(t *testing.T) {
		base := "/api/events?source_id=" + uuidString(srcA)
		page1 := listOK(t, base+"&limit=2")
		if got := ids(page1); !equalIDs(got, []string{uuidString(e3), uuidString(e2)}) {
			t.Fatalf("page 1 = %v, want e3,e2", got)
		}
		if page1.NextCursor == "" {
			t.Fatal("page 1 has no next_cursor")
		}

		// A new event arriving between pages must not shift page 2: it sorts
		// ahead of the cursor, so page 2 still yields exactly the older tail.
		insert(srcA, true, `{"marker":"`+marker+`","kind":"late"}`)

		page2 := listOK(t, base+"&limit=2&cursor="+page1.NextCursor)
		if got := ids(page2); !equalIDs(got, []string{uuidString(e1)}) {
			t.Errorf("page 2 = %v, want just e1 (the late insert must not appear)", got)
		}
	})

	t.Run("get full event includes raw headers and body", func(t *testing.T) {
		rec := get("/api/events/" + uuidString(e1))
		if rec.Code != http.StatusOK {
			t.Fatalf("get status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var detail eventDetail
		if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
			t.Fatalf("decoding detail: %v", err)
		}
		if string(detail.RawBody) != `{"marker":"`+marker+`","kind":"payment"}` {
			t.Errorf("raw_body = %q, not byte-exact", string(detail.RawBody))
		}
		if len(detail.RawHeaders) == 0 || !detail.Verified {
			t.Errorf("missing headers or verified flag: %+v", detail)
		}
	})

	t.Run("unknown event is 404", func(t *testing.T) {
		if rec := get("/api/events/00000000-0000-7000-8000-0000000000ff"); rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

// TestEventTraceAPIIntegration covers the per-event trace (#19): an event that
// fails twice then succeeds must surface three attempts with their status
// codes, errors, and durations.
func TestEventTraceAPIIntegration(t *testing.T) {
	pool := testDB(t)
	q := db.New(pool)
	ctx := context.Background()

	src := insertEventsTestSource(t, pool, q)
	ev, err := q.InsertEvent(ctx, db.InsertEventParams{
		TenantID:   tenancy.DefaultTenantID,
		SourceID:   src,
		RawHeaders: []byte("{}"),
		RawBody:    []byte(`{"kind":"trace"}`),
		Verified:   true,
	})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}

	dest, err := q.InsertDestination(ctx, db.InsertDestinationParams{
		TenantID:           tenancy.DefaultTenantID,
		Name:               "trace-dest",
		Url:                "https://example.test/hook",
		AuthConfig:         []byte("{}"),
		TimeoutMs:          defaultTimeoutMs,
		MaxAttempts:        defaultMaxAttempts,
		BackoffBaseSeconds: defaultBackoffBaseSeconds,
		BackoffMaxSeconds:  defaultBackoffMaxSeconds,
	})
	if err != nil {
		t.Fatalf("insert destination: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM destinations WHERE id = $1", dest.ID) })

	deliveryID, err := q.InsertDelivery(ctx, db.InsertDeliveryParams{
		TenantID:      tenancy.DefaultTenantID,
		EventID:       ev.ID,
		DestinationID: dest.ID,
	})
	if err != nil {
		t.Fatalf("insert delivery: %v", err)
	}

	// Two failures (a 500, then a connection error with no HTTP response) then a
	// 200 success — the trace should show all three.
	attempts := []db.InsertDeliveryAttemptParams{
		{DeliveryID: deliveryID, AttemptNumber: 1, RequestHeaders: []byte(`{"X-Try":"1"}`),
			ResponseStatusCode: pgtype.Int4{Int32: 500, Valid: true}, DurationMs: pgtype.Int4{Int32: 12, Valid: true}},
		{DeliveryID: deliveryID, AttemptNumber: 2, RequestHeaders: []byte(`{"X-Try":"2"}`),
			Error: pgtype.Text{String: "connection refused", Valid: true}, DurationMs: pgtype.Int4{Int32: 5, Valid: true}},
		{DeliveryID: deliveryID, AttemptNumber: 3, RequestHeaders: []byte(`{"X-Try":"3"}`),
			ResponseStatusCode: pgtype.Int4{Int32: 200, Valid: true}, DurationMs: pgtype.Int4{Int32: 8, Valid: true}},
	}
	for _, a := range attempts {
		if err := q.InsertDeliveryAttempt(ctx, a); err != nil {
			t.Fatalf("insert attempt %d: %v", a.AttemptNumber, err)
		}
	}
	if _, err := pool.Exec(ctx,
		"UPDATE deliveries SET status = 'succeeded', attempt_count = 3 WHERE id = $1", deliveryID); err != nil {
		t.Fatalf("mark delivery succeeded: %v", err)
	}

	const adminPassword = "test-admin-password"
	mux := http.NewServeMux()
	RegisterEvents(mux, q, middleware.NewAuth(q, adminPassword))

	req := httptest.NewRequest(http.MethodGet, "/api/events/"+uuidString(ev.ID)+"/trace", nil)
	req.Header.Set("Authorization", "Bearer "+adminPassword)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("trace status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var trace eventTrace
	if err := json.Unmarshal(rec.Body.Bytes(), &trace); err != nil {
		t.Fatalf("decoding trace: %v", err)
	}
	if !trace.Verified {
		t.Error("trace verified = false, want true")
	}
	if len(trace.Deliveries) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(trace.Deliveries))
	}
	d := trace.Deliveries[0]
	if d.Status != "succeeded" {
		t.Errorf("delivery status = %q, want succeeded", d.Status)
	}
	if len(d.Attempts) != 3 {
		t.Fatalf("attempts = %d, want 3", len(d.Attempts))
	}
	if got := d.Attempts[0].ResponseStatusCode; got == nil || *got != 500 {
		t.Errorf("attempt 1 status = %v, want 500", got)
	}
	if d.Attempts[1].Error != "connection refused" || d.Attempts[1].ResponseStatusCode != nil {
		t.Errorf("attempt 2 = %+v, want a connection error with no status", d.Attempts[1])
	}
	if got := d.Attempts[2].ResponseStatusCode; got == nil || *got != 200 {
		t.Errorf("attempt 3 status = %v, want 200", got)
	}
	for i, a := range d.Attempts {
		if a.DurationMs == nil {
			t.Errorf("attempt %d missing duration", i+1)
		}
	}
}

// --- helpers ---

func insertEventsTestSource(t *testing.T, pool *pgxpool.Pool, q *db.Queries) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	src, err := q.InsertSource(ctx, db.InsertSourceParams{
		TenantID:           tenancy.DefaultTenantID,
		Name:               "events-it-src",
		ProviderType:       "none",
		EndpointPath:       "src_events_it_" + randHex(t),
		VerificationConfig: []byte("{}"),
	})
	if err != nil {
		t.Fatalf("insert source: %v", err)
	}
	// events, deliveries, and attempts all cascade from the source.
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM sources WHERE id = $1", src.ID) })
	return src.ID
}

func ids(resp listEventsResponse) []string {
	out := make([]string, len(resp.Events))
	for i, e := range resp.Events {
		out[i] = e.ID
	}
	return out
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
