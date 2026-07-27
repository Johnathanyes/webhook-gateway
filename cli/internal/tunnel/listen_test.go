package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// safeBuffer collects log output written from the session's goroutines.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func writeJSON(t *testing.T, ctx context.Context, ws *websocket.Conn, frame any) {
	t.Helper()
	data, err := json.Marshal(frame)
	if err != nil {
		t.Errorf("marshal frame: %v", err)
		return
	}
	if err := ws.Write(ctx, websocket.MessageText, data); err != nil {
		t.Errorf("write frame: %v", err)
	}
}

// The #28 done-test in miniature: a fake gateway pushes an event, the client
// forwards it to a local sink, and acks with the sink's status code.
func TestListenForwardsEventAndAcks(t *testing.T) {
	var gotBody, gotSignature string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		gotBody, gotSignature = string(body), r.Header.Get("Stripe-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()

	acks := make(chan AckFrame, 1)
	var gotAuth string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		ctx := r.Context()

		writeJSON(t, ctx, ws, ReadyFrame{Type: FrameReady, DestinationID: "dest-1", Sources: []string{"stripe"}})
		writeJSON(t, ctx, ws, EventFrame{
			Type:       FrameEvent,
			DeliveryID: "del-1",
			EventID:    "evt-1",
			Source:     "stripe",
			Headers: map[string][]string{
				"Content-Type":     {"application/json"},
				"Stripe-Signature": {"t=1,v1=abc"},
			},
			Body: []byte(`{"type":"payment_intent.succeeded"}`),
		})

		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var ack AckFrame
		if err := json.Unmarshal(data, &ack); err == nil {
			select {
			case acks <- ack:
			default:
			}
		}
		<-ctx.Done()
	}))
	defer gateway.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := &safeBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- Listen(ctx, Options{
			GatewayURL: gateway.URL,
			APIKey:     "whg_test",
			Source:     "stripe",
			ForwardTo:  sink.URL,
			Out:        out,
		})
	}()

	var ack AckFrame
	select {
	case ack = <-acks:
	case <-time.After(10 * time.Second):
		t.Fatalf("no ack within 10s; output:\n%s", out.String())
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Listen returned %v, want nil on cancel", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("Listen did not return after cancel")
	}

	if gotAuth != "Bearer whg_test" {
		t.Errorf("gateway saw Authorization %q, want %q", gotAuth, "Bearer whg_test")
	}
	if ack.Type != FrameAck || ack.DeliveryID != "del-1" {
		t.Errorf("ack = %+v, want an ack for del-1", ack)
	}
	if ack.StatusCode != http.StatusOK {
		t.Errorf("ack status = %d, want 200", ack.StatusCode)
	}
	if ack.Error != "" {
		t.Errorf("ack carried error %q, want none", ack.Error)
	}
	if gotBody != `{"type":"payment_intent.succeeded"}` {
		t.Errorf("sink body = %q, want the original payload", gotBody)
	}
	if gotSignature != "t=1,v1=abc" {
		t.Errorf("sink Stripe-Signature = %q, want it preserved", gotSignature)
	}

	// The readable-log requirement: source, event type, status, duration.
	logged := out.String()
	if !strings.Contains(logged, "stripe payment_intent.succeeded → 200") {
		t.Errorf("log line missing or malformed:\n%s", logged)
	}
	if !strings.Contains(logged, "tunnel ready") {
		t.Errorf("no ready banner:\n%s", logged)
	}
}

// A local server that isn't running must produce an error ack, not a hang —
// this is the single most common state during development.
func TestListenAcksUnreachableLocalTarget(t *testing.T) {
	acks := make(chan AckFrame, 1)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		ctx := r.Context()

		writeJSON(t, ctx, ws, ReadyFrame{Type: FrameReady, Sources: []string{"stripe"}})
		writeJSON(t, ctx, ws, EventFrame{
			Type: FrameEvent, DeliveryID: "del-2", Source: "stripe", Body: []byte(`{}`),
		})

		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var ack AckFrame
		if err := json.Unmarshal(data, &ack); err == nil {
			select {
			case acks <- ack:
			default:
			}
		}
		<-ctx.Done()
	}))
	defer gateway.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := &safeBuffer{}
	go func() {
		_ = Listen(ctx, Options{
			GatewayURL: gateway.URL,
			APIKey:     "whg_test",
			ForwardTo:  "http://127.0.0.1:1", // refuses connections
			Out:        out,
		})
	}()

	select {
	case ack := <-acks:
		if ack.Error == "" {
			t.Errorf("ack = %+v, want an error for an unreachable target", ack)
		}
		if ack.StatusCode != 0 {
			t.Errorf("ack status = %d, want 0 when the target was never reached", ack.StatusCode)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("no ack within 10s; output:\n%s", out.String())
	}
}

// A dropped socket — gateway restart, laptop sleep — must reconnect on its own,
// since a tunnel is meant to be left running for a whole work session.
func TestListenReconnectsAfterDrop(t *testing.T) {
	var mu sync.Mutex
	connections := 0
	ready := make(chan struct{}, 2)

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		connections++
		attempt := connections
		mu.Unlock()

		if attempt == 1 {
			// Drop the first connection the moment it is established.
			_ = ws.CloseNow()
			return
		}
		defer func() { _ = ws.CloseNow() }()
		writeJSON(t, r.Context(), ws, ReadyFrame{Type: FrameReady, Sources: []string{"stripe"}})
		ready <- struct{}{}
		<-r.Context().Done()
	}))
	defer gateway.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := &safeBuffer{}
	start := time.Now()
	go func() {
		_ = Listen(ctx, Options{
			GatewayURL: gateway.URL,
			APIKey:     "whg_test",
			ForwardTo:  "http://127.0.0.1:1",
			Out:        out,
		})
	}()

	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		t.Fatalf("never reconnected; output:\n%s", out.String())
	}

	// Backoff jitters to 50–100% of the 1s minimum, so a reconnect must not be
	// instantaneous — otherwise a down gateway would be hammered.
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond {
		t.Errorf("reconnected after %s, want at least the jittered backoff", elapsed)
	}
	if logged := out.String(); !strings.Contains(logged, "connection lost") {
		t.Errorf("drop was not reported to the user:\n%s", logged)
	}

	mu.Lock()
	defer mu.Unlock()
	if connections < 2 {
		t.Errorf("connections = %d, want at least 2", connections)
	}
}

// A missing scope can never be fixed by reconnecting, so listen must exit
// rather than spin against the gateway.
func TestListenExitsOnPermanentFailure(t *testing.T) {
	var attempts int
	var mu sync.Mutex
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"missing scope: tunnel"}`))
	}))
	defer gateway.Close()

	done := make(chan error, 1)
	go func() {
		done <- Listen(context.Background(), Options{
			GatewayURL: gateway.URL,
			APIKey:     "whg_noscope",
			ForwardTo:  "http://127.0.0.1:1",
			Out:        &safeBuffer{},
		})
	}()

	select {
	case err := <-done:
		var permanent *PermanentError
		if !errors.As(err, &permanent) {
			t.Fatalf("Listen returned %v (%T), want *PermanentError", err, err)
		}
		if !strings.Contains(permanent.Message, "`tunnel` scope") {
			t.Errorf("error = %q, want it to name the missing scope", permanent.Message)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Listen retried a 403 instead of exiting")
	}

	mu.Lock()
	defer mu.Unlock()
	if attempts != 1 {
		t.Errorf("dial attempts = %d, want exactly 1", attempts)
	}
}
