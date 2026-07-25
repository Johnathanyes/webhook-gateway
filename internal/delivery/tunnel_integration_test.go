package delivery

// Task #26 verification: a raw WebSocket client attaches to /api/tunnel, an
// event ingested through the real path arrives over the socket, and the
// client's ack drives the delivery to succeeded through the normal
// delivery-status machinery. Skipped unless TEST_DATABASE_URL is set:
//
//	make test-integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgtype"

	"webhook-gateway/internal/db"
	"webhook-gateway/internal/tenancy"
	"webhook-gateway/internal/tunnel"
)

// tunnelSource creates a source with a unique name, since the tunnel endpoint
// selects sources by name and other tests reuse "e2e-source".
func (h *harness) tunnelSource(t *testing.T) db.Source {
	t.Helper()
	suffix := randomHex(t)
	src, err := h.q.InsertSource(context.Background(), db.InsertSourceParams{
		TenantID:           tenancy.DefaultTenantID,
		Name:               "tunnel-src-" + suffix,
		ProviderType:       "none",
		EndpointPath:       "src_" + suffix,
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

// dialTunnel starts a real listener in front of the harness mux (a WebSocket
// needs an actual socket, not httptest.NewRequest) and attaches to the tunnel
// for one source, returning the connection and the ready frame.
func dialTunnel(t *testing.T, ctx context.Context, h *harness, sourceName string) (*websocket.Conn, tunnel.ReadyFrame) {
	t.Helper()
	srv := httptest.NewServer(h.mux)
	t.Cleanup(srv.Close)

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/tunnel?source=" + sourceName
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + testAdminPassword}},
	})
	if err != nil {
		t.Fatalf("dialing tunnel: %v", err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })

	var ready tunnel.ReadyFrame
	readFrame(t, ctx, ws, &ready)
	if ready.Type != tunnel.FrameReady {
		t.Fatalf("first frame type = %q, want %q", ready.Type, tunnel.FrameReady)
	}
	return ws, ready
}

func readFrame(t *testing.T, ctx context.Context, ws *websocket.Conn, into any) {
	t.Helper()
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("reading frame: %v", err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatalf("decoding frame %s: %v", data, err)
	}
}

func writeFrame(t *testing.T, ctx context.Context, ws *websocket.Conn, frame any) {
	t.Helper()
	data, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("encoding frame: %v", err)
	}
	if err := ws.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("writing frame: %v", err)
	}
}

func destinationExists(t *testing.T, h *harness, destID string) bool {
	t.Helper()
	var n int
	if err := h.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM destinations WHERE id = $1", destID).Scan(&n); err != nil {
		t.Fatalf("count destinations: %v", err)
	}
	return n > 0
}

// BR-21: an attached tunnel is a destination. An event fans out to it, arrives
// over the socket with the provider's original headers and byte-exact body,
// and the client's 200 ack marks the delivery succeeded.
func TestTunnelDeliversAndAcks(t *testing.T) {
	h := newHarness(t)
	h.startWorker(t)
	src := h.tunnelSource(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ws, ready := dialTunnel(t, ctx, h, src.Name)
	if len(ready.Sources) != 1 || ready.Sources[0] != src.Name {
		t.Fatalf("ready.Sources = %v, want [%s]", ready.Sources, src.Name)
	}

	const body = `{"marker":"tunnel-e2e","amount":4200}`
	eventID := h.postEvent(t, src, body)

	var frame tunnel.EventFrame
	readFrame(t, ctx, ws, &frame)

	if frame.Type != tunnel.FrameEvent {
		t.Errorf("frame type = %q, want %q", frame.Type, tunnel.FrameEvent)
	}
	if frame.EventID != uuidStr(eventID) {
		t.Errorf("frame event id = %q, want %q", frame.EventID, uuidStr(eventID))
	}
	if frame.Source != src.Name {
		t.Errorf("frame source = %q, want %q", frame.Source, src.Name)
	}
	if string(frame.Body) != body {
		t.Errorf("frame body = %q, want %q", frame.Body, body)
	}
	// The provider's original headers ride along — this is what lets a local
	// handler verify a real signature, and is what HTTP dispatch does not do.
	if got := frame.Headers["Content-Type"]; len(got) != 1 || got[0] != "application/json" {
		t.Errorf("frame Content-Type header = %v, want [application/json]", got)
	}

	writeFrame(t, ctx, ws, tunnel.AckFrame{
		Type:       tunnel.FrameAck,
		DeliveryID: frame.DeliveryID,
		StatusCode: http.StatusOK,
		DurationMs: 12,
	})

	var delivery deliveryRow
	eventually(t, 15*time.Second, "tunnel delivery reaches succeeded", func() bool {
		rows := deliveriesForEvent(t, h.pool, eventID)
		if len(rows) != 1 {
			return false
		}
		delivery = rows[0]
		return delivery.status == statusSucceeded
	})

	if delivery.attemptCount != 1 {
		t.Errorf("attempt count = %d, want 1", delivery.attemptCount)
	}
	// The ack is recorded exactly as an HTTP response would be, so the trace
	// view (#19) works on a tunnel with no special-casing.
	var status pgtype.Int4
	if err := h.pool.QueryRow(context.Background(),
		"SELECT response_status_code FROM delivery_attempts WHERE delivery_id = $1", delivery.id).Scan(&status); err != nil {
		t.Fatalf("query attempt: %v", err)
	}
	if !status.Valid || status.Int32 != http.StatusOK {
		t.Errorf("recorded attempt status = %v, want 200", status)
	}
}

// A disconnected tunnel is a destination that no longer exists: its row (and,
// by cascade, its routes and any queued delivery) goes away, so nothing is left
// retrying against a socket that is gone.
func TestTunnelDisconnectRemovesDestination(t *testing.T) {
	h := newHarness(t)
	src := h.tunnelSource(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ws, ready := dialTunnel(t, ctx, h, src.Name)
	if !destinationExists(t, h, ready.DestinationID) {
		t.Fatal("ephemeral destination missing while tunnel attached")
	}

	if err := ws.Close(websocket.StatusNormalClosure, "done"); err != nil {
		t.Fatalf("closing tunnel: %v", err)
	}

	eventually(t, 10*time.Second, "ephemeral destination is cleaned up", func() bool {
		return !destinationExists(t, h, ready.DestinationID)
	})

	var routes int
	if err := h.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM routes WHERE source_id = $1", src.ID).Scan(&routes); err != nil {
		t.Fatalf("count routes: %v", err)
	}
	if routes != 0 {
		t.Errorf("routes remaining after disconnect = %d, want 0", routes)
	}
}
