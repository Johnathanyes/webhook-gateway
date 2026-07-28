package tunnel

import (
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

// The handler's attach/route/detach flow needs Postgres and a delivery worker;
// it is covered end-to-end by TestTunnelDeliversAndAcks and
// TestTunnelDisconnectRemovesDestination in internal/delivery. What is tested
// here is the socket machinery underneath: the registry, the ack correlation in
// Conn, and the wire types themselves.

// wsPair opens a real WebSocket and returns the server-side Conn with its
// readLoop already running, plus the client socket the test drives it from.
// A real socket rather than a fake keeps the framing, concurrency, and close
// semantics honest — those are exactly what Conn is responsible for.
func wsPair(t *testing.T) (*Conn, *websocket.Conn) {
	t.Helper()

	conns := make(chan *Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		c := newConn(ws)
		conns <- c
		_ = c.readLoop(r.Context())
	}))
	// Registered first, so it runs last: the client socket below closes before
	// the server does, which lets the handler return instead of Close blocking.
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dialing tunnel: %v", err)
	}
	t.Cleanup(func() { _ = client.CloseNow() })

	select {
	case c := <-conns:
		return c, client
	case <-time.After(5 * time.Second):
		t.Fatal("server never accepted the websocket")
		return nil, nil
	}
}

func clientRead(t *testing.T, ws *websocket.Conn) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	return data
}

func clientWrite(t *testing.T, ws *websocket.Conn, payload []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatalf("client write: %v", err)
	}
}

func clientWriteJSON(t *testing.T, ws *websocket.Conn, v any) {
	t.Helper()
	payload, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling client frame: %v", err)
	}
	clientWrite(t, ws, payload)
}

// deliverResult carries Deliver's return values off the goroutine that runs it,
// so the test goroutine keeps ownership of t.
type deliverResult struct {
	ack AckFrame
	err error
}

func deliverAsync(ctx context.Context, c *Conn, frame EventFrame) <-chan deliverResult {
	out := make(chan deliverResult, 1)
	go func() {
		ack, err := c.Deliver(ctx, frame)
		out <- deliverResult{ack: ack, err: err}
	}()
	return out
}

func testFrame(deliveryID string) EventFrame {
	return EventFrame{
		Type:       FrameEvent,
		DeliveryID: deliveryID,
		EventID:    "evt-1",
		Source:     "stripe",
		Headers:    map[string][]string{"Stripe-Signature": {"t=1,v1=abc"}},
		Body:       []byte(`{"hello":"tunnel"}`),
	}
}

func TestIsURL(t *testing.T) {
	tests := map[string]bool{
		"tunnel://019fa15e":       true,
		"tunnel://":               true,
		"https://example.com":     false,
		"http://tunnel://nope":    false,
		"":                        false,
		"TUNNEL://uppercase-fail": false,
	}
	for url, want := range tests {
		if got := IsURL(url); got != want {
			t.Errorf("IsURL(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestRegistryRegisterLookupUnregister(t *testing.T) {
	r := NewRegistry()
	id := [16]byte{1, 2, 3}

	if _, ok := r.Lookup(id); ok {
		t.Fatal("Lookup found a connection on an empty registry")
	}

	conn := &Conn{}
	r.register(id, conn)
	got, ok := r.Lookup(id)
	if !ok {
		t.Fatal("Lookup missed a registered connection")
	}
	if got != conn {
		t.Error("Lookup returned a different connection than was registered")
	}

	// A different destination must not resolve to this socket — the registry is
	// what keeps one tunnel's events out of another's.
	if _, ok := r.Lookup([16]byte{9, 9, 9}); ok {
		t.Error("Lookup matched an unregistered destination")
	}

	r.unregister(id)
	if _, ok := r.Lookup(id); ok {
		t.Error("Lookup found a connection after unregister")
	}
}

// The delivery worker looks up concurrently with tunnels attaching and
// detaching, so the map access has to be safe under -race.
func TestRegistryConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	const n = 50

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := [16]byte{byte(i)}
			conn := &Conn{}
			r.register(id, conn)
			r.Lookup(id)
			r.unregister(id)
		}()
	}
	wg.Wait()

	for i := range n {
		if _, ok := r.Lookup([16]byte{byte(i)}); ok {
			t.Fatalf("connection %d survived unregister", i)
		}
	}
}

func TestConnDeliverReturnsMatchingAck(t *testing.T) {
	c, client := wsPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := deliverAsync(ctx, c, testFrame("d-1"))

	var sent EventFrame
	if err := json.Unmarshal(clientRead(t, client), &sent); err != nil {
		t.Fatalf("decoding event frame: %v", err)
	}
	if sent.Type != FrameEvent || sent.DeliveryID != "d-1" {
		t.Errorf("client received type=%q delivery_id=%q, want %q/%q", sent.Type, sent.DeliveryID, FrameEvent, "d-1")
	}
	if string(sent.Body) != `{"hello":"tunnel"}` {
		t.Errorf("body = %q, want the original payload", sent.Body)
	}
	// A tunnel exists so a local handler is exercised as production would be,
	// signature headers included.
	if got := sent.Headers["Stripe-Signature"]; len(got) != 1 || got[0] != "t=1,v1=abc" {
		t.Errorf("Stripe-Signature = %v, want the provider's original value", got)
	}

	clientWriteJSON(t, client, AckFrame{Type: FrameAck, DeliveryID: "d-1", StatusCode: 200, DurationMs: 42})

	got := <-result
	if got.err != nil {
		t.Fatalf("Deliver: %v", got.err)
	}
	if got.ack.StatusCode != 200 || got.ack.DurationMs != 42 {
		t.Errorf("ack = %+v, want status 200 duration 42", got.ack)
	}
}

// The caller's context carries the destination timeout: a client that never
// acks must fail its delivery rather than pin a worker slot forever.
func TestConnDeliverTimesOutWithoutAck(t *testing.T) {
	c, client := wsPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	result := deliverAsync(ctx, c, testFrame("d-timeout"))
	clientRead(t, client) // the event arrives; the client simply never acks

	select {
	case got := <-result:
		if !errors.Is(got.err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want context.DeadlineExceeded", got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Deliver never returned after its context expired")
	}
}

// A disconnected tunnel is a destination that no longer exists, so an in-flight
// delivery is woken with ErrClosed instead of waiting for its timeout.
func TestConnDeliverWakesInFlightOnDisconnect(t *testing.T) {
	c, client := wsPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := deliverAsync(ctx, c, testFrame("d-inflight"))
	clientRead(t, client) // ensure the delivery is registered before closing

	_ = client.CloseNow()

	select {
	case got := <-result:
		if !errors.Is(got.err, ErrClosed) {
			t.Errorf("err = %v, want ErrClosed", got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Deliver did not wake when the socket closed")
	}
}

func TestConnDeliverAfterDisconnectReturnsErrClosed(t *testing.T) {
	c, client := wsPair(t)
	_ = client.CloseNow()

	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("readLoop never ended after the client disconnected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Deliver(ctx, testFrame("d-late")); !errors.Is(err, ErrClosed) {
		t.Errorf("err = %v, want ErrClosed", err)
	}
}

// A malformed or unknown frame is the client's bug; it must not tear down a
// working tunnel, so a valid ack after the garbage still resolves its delivery.
func TestConnIgnoresUnrecognizedFrames(t *testing.T) {
	c, client := wsPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := deliverAsync(ctx, c, testFrame("d-survives"))
	clientRead(t, client)

	clientWrite(t, client, []byte("this is not json"))
	clientWriteJSON(t, client, map[string]string{"type": "who-knows"})
	clientWriteJSON(t, client, AckFrame{Type: FrameAck, DeliveryID: "not-a-real-delivery", StatusCode: 500})
	clientWriteJSON(t, client, AckFrame{Type: FrameAck, DeliveryID: "d-survives", StatusCode: 204})

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("Deliver: %v", got.err)
		}
		if got.ack.StatusCode != 204 {
			t.Errorf("status = %d, want 204 — the delivery resolved with the wrong ack", got.ack.StatusCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a malformed frame killed the tunnel")
	}
}

// Duplicate acks are discarded rather than blocking the reader: the pending
// channel has exactly one slot.
func TestConnDiscardsDuplicateAcks(t *testing.T) {
	c, client := wsPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := deliverAsync(ctx, c, testFrame("d-dup"))
	clientRead(t, client)

	for range 3 {
		clientWriteJSON(t, client, AckFrame{Type: FrameAck, DeliveryID: "d-dup", StatusCode: 200})
	}

	got := <-result
	if got.err != nil {
		t.Fatalf("Deliver: %v", got.err)
	}

	// The readLoop is still healthy after the extra acks: a second delivery
	// completes normally.
	second := deliverAsync(ctx, c, testFrame("d-dup-2"))
	clientRead(t, client)
	clientWriteJSON(t, client, AckFrame{Type: FrameAck, DeliveryID: "d-dup-2", StatusCode: 201})
	if got := <-second; got.err != nil || got.ack.StatusCode != 201 {
		t.Errorf("second delivery = %+v, err = %v; want status 201", got.ack, got.err)
	}
}

// Body is []byte so encoding/json base64s it: a non-UTF-8 payload has to survive
// a text frame byte-for-byte, which is what makes raw XML and binary bodies
// replayable.
func TestEventFrameBodySurvivesNonUTF8(t *testing.T) {
	body := []byte{0xff, 0xfe, 0x00, 0x7b, 0x7d}
	encoded, err := json.Marshal(EventFrame{Type: FrameEvent, DeliveryID: "d-bin", Body: body})
	if err != nil {
		t.Fatalf("marshalling frame: %v", err)
	}

	var decoded EventFrame
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshalling frame: %v", err)
	}
	if string(decoded.Body) != string(body) {
		t.Errorf("body round-tripped as %v, want %v", decoded.Body, body)
	}
}
