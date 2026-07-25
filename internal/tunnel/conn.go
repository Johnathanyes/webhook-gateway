package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/coder/websocket"
)

// ErrClosed is returned when a delivery is handed to a tunnel whose socket has
// already gone away. The worker treats it as terminal: a disconnected tunnel is
// a destination that no longer exists, not a destination that is briefly down.
var ErrClosed = errors.New("tunnel connection closed")

// Conn is one attached CLI. Deliver pushes an event frame and blocks until the
// matching ack arrives, so the delivery worker's normal
// "dispatch → record attempt" flow works unchanged.
type Conn struct {
	ws      *websocket.Conn
	writeMu sync.Mutex

	// pending correlates in-flight deliveries with their acks. A nil map means
	// the connection is closed and no new deliveries may be registered.
	mu      sync.Mutex
	pending map[string]chan AckFrame

	done chan struct{}
}

func newConn(ws *websocket.Conn) *Conn {
	return &Conn{
		ws:      ws,
		pending: make(map[string]chan AckFrame),
		done:    make(chan struct{}),
	}
}

// Deliver writes one event frame and waits for its ack. The caller's context
// carries the destination's timeout, so a client that never acks fails the
// delivery rather than pinning a worker slot forever.
func (c *Conn) Deliver(ctx context.Context, frame EventFrame) (AckFrame, error) {
	ackCh := make(chan AckFrame, 1)

	c.mu.Lock()
	if c.pending == nil {
		c.mu.Unlock()
		return AckFrame{}, ErrClosed
	}
	c.pending[frame.DeliveryID] = ackCh
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, frame.DeliveryID)
		c.mu.Unlock()
	}()

	if err := c.write(ctx, frame); err != nil {
		return AckFrame{}, err
	}

	select {
	case ack := <-ackCh:
		return ack, nil
	case <-c.done:
		return AckFrame{}, ErrClosed
	case <-ctx.Done():
		return AckFrame{}, ctx.Err()
	}
}

// write serializes access to the socket: deliveries run concurrently across
// worker goroutines, but a WebSocket permits only one writer at a time.
func (c *Conn) write(ctx context.Context, frame any) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.Write(ctx, websocket.MessageText, payload)
}

// readLoop consumes ack frames until the socket errors or closes, then wakes
// every in-flight Deliver. It returns the read error that ended the session.
func (c *Conn) readLoop(ctx context.Context) error {
	defer c.closePending()
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			return err
		}

		var ack AckFrame
		if err := json.Unmarshal(data, &ack); err != nil || ack.Type != FrameAck {
			// A malformed or unknown frame is the client's bug; drop it rather
			// than tearing down a working tunnel over it.
			slog.Warn("tunnel: ignoring unrecognized client frame", "error", err)
			continue
		}

		c.mu.Lock()
		ch, ok := c.pending[ack.DeliveryID]
		c.mu.Unlock()
		if !ok {
			// Late ack for a delivery that already timed out.
			continue
		}
		// Buffered channel with exactly one slot; a duplicate ack is discarded.
		select {
		case ch <- ack:
		default:
		}
	}
}

func (c *Conn) closePending() {
	c.mu.Lock()
	c.pending = nil
	c.mu.Unlock()
	close(c.done)
}
