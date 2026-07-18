package delivery

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"webhook-gateway/internal/db"
)

func testEvent() db.GetEventForDeliveryRow {
	return db.GetEventForDeliveryRow{
		RawBody:     []byte(`{"hello":"world"}`),
		ContentType: text("application/json"),
	}
}

// TestDispatchSuccess: a 2xx destination yields a successful attempt carrying
// the response, and the destination receives the verbatim body + content type.
func TestDispatchSuccess(t *testing.T) {
	var gotBody []byte
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	w := NewWorker(nil, nil)
	dest := db.Destination{Url: srv.URL, TimeoutMs: 5000}

	res := w.dispatch(context.Background(), dest, testEvent())

	if !res.succeeded {
		t.Fatalf("succeeded = false, want true; errMsg=%q", res.errMsg.String)
	}
	if res.statusCode.Int32 != http.StatusOK {
		t.Errorf("status = %d, want 200", res.statusCode.Int32)
	}
	if res.responseBody.String != "ok" {
		t.Errorf("response body = %q, want %q", res.responseBody.String, "ok")
	}
	if string(gotBody) != `{"hello":"world"}` {
		t.Errorf("destination got body %q, want the verbatim payload", gotBody)
	}
	if gotContentType != "application/json" {
		t.Errorf("destination got content-type %q, want application/json", gotContentType)
	}
	if !res.durationMs.Valid {
		t.Errorf("durationMs not recorded")
	}
}

// TestDispatchNon2xx: a non-2xx response is a failure that still records the
// status code for the trace.
func TestDispatchNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	w := NewWorker(nil, nil)
	dest := db.Destination{Url: srv.URL, TimeoutMs: 5000}

	res := w.dispatch(context.Background(), dest, testEvent())

	if res.succeeded {
		t.Fatalf("succeeded = true, want false for a 500")
	}
	if res.statusCode.Int32 != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", res.statusCode.Int32)
	}
	if !res.errMsg.Valid {
		t.Errorf("errMsg not set for a failed attempt")
	}
}

// TestDispatchTimeout: a destination slower than its timeout is treated as a
// failure with no status code (BR-11), and dispatch returns near the timeout
// rather than waiting for the slow response.
func TestDispatchTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := NewWorker(nil, nil)
	dest := db.Destination{Url: srv.URL, TimeoutMs: 50}

	start := time.Now()
	res := w.dispatch(context.Background(), dest, testEvent())
	elapsed := time.Since(start)

	if res.succeeded {
		t.Fatalf("succeeded = true, want false on timeout")
	}
	if res.statusCode.Valid {
		t.Errorf("status code = %d recorded, want none for a timeout", res.statusCode.Int32)
	}
	if !res.errMsg.Valid {
		t.Errorf("errMsg not set on timeout")
	}
	if elapsed > 300*time.Millisecond {
		t.Errorf("dispatch took %s, want it to abort near the 50ms timeout", elapsed)
	}
}

// TestDispatchDefaultTimeout: a non-positive timeout_ms falls back to the guard
// rather than issuing a request with a zero deadline that fails instantly.
func TestDispatchDefaultTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := NewWorker(nil, nil)
	dest := db.Destination{Url: srv.URL, TimeoutMs: 0}

	res := w.dispatch(context.Background(), dest, testEvent())

	if !res.succeeded {
		t.Fatalf("succeeded = false with fallback timeout; errMsg=%q", res.errMsg.String)
	}
}
