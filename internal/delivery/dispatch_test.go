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
// the response, and the destination receives the verbatim body, content type,
// and the Webhook-Id dedupe header.
func TestDispatchSuccess(t *testing.T) {
	var gotBody []byte
	var gotContentType, gotWebhookID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotContentType = r.Header.Get("Content-Type")
		gotWebhookID = r.Header.Get("Webhook-Id")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	w := NewWorker(nil, nil)
	dest := db.Destination{Url: srv.URL, TimeoutMs: 5000}

	res := w.dispatch(context.Background(), dest, testEvent(), "delivery-123")

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
	if gotWebhookID != "delivery-123" {
		t.Errorf("destination got Webhook-Id %q, want delivery-123", gotWebhookID)
	}
	if !res.durationMs.Valid {
		t.Errorf("durationMs not recorded")
	}
}

// TestDispatchRetryable5xx: a 5xx is a retryable failure that still records the
// status code for the trace.
func TestDispatchRetryable5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	w := NewWorker(nil, nil)
	dest := db.Destination{Url: srv.URL, TimeoutMs: 5000}

	res := w.dispatch(context.Background(), dest, testEvent(), "d")

	if res.succeeded {
		t.Fatalf("succeeded = true, want false for a 500")
	}
	if !res.retryable {
		t.Errorf("retryable = false, want true for a 500")
	}
	if res.statusCode.Int32 != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", res.statusCode.Int32)
	}
}

// TestDispatchTerminal4xx: a 4xx (other than 408/429) is a terminal failure —
// retrying won't help, so it must not be marked retryable.
func TestDispatchTerminal4xx(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusUnprocessableEntity} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		res := NewWorker(nil, nil).dispatch(context.Background(), db.Destination{Url: srv.URL, TimeoutMs: 5000}, testEvent(), "d")
		srv.Close()

		if res.succeeded {
			t.Errorf("status %d: succeeded = true, want false", code)
		}
		if res.retryable {
			t.Errorf("status %d: retryable = true, want false (terminal)", code)
		}
	}
}

// TestDispatchRetryable429: 429 is transient backpressure, so retryable.
func TestDispatchRetryable429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	res := NewWorker(nil, nil).dispatch(context.Background(), db.Destination{Url: srv.URL, TimeoutMs: 5000}, testEvent(), "d")
	if res.succeeded || !res.retryable {
		t.Errorf("429: succeeded=%v retryable=%v, want false/true", res.succeeded, res.retryable)
	}
}

// TestDispatchTimeout: a destination slower than its timeout is a retryable
// failure with no status code (BR-11), and dispatch aborts near the timeout.
func TestDispatchTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := NewWorker(nil, nil)
	dest := db.Destination{Url: srv.URL, TimeoutMs: 50}

	start := time.Now()
	res := w.dispatch(context.Background(), dest, testEvent(), "d")
	elapsed := time.Since(start)

	if res.succeeded {
		t.Fatalf("succeeded = true, want false on timeout")
	}
	if !res.retryable {
		t.Errorf("retryable = false, want true for a timeout")
	}
	if res.statusCode.Valid {
		t.Errorf("status code = %d recorded, want none for a timeout", res.statusCode.Int32)
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

	res := w.dispatch(context.Background(), dest, testEvent(), "d")

	if !res.succeeded {
		t.Fatalf("succeeded = false with fallback timeout; errMsg=%q", res.errMsg.String)
	}
}
