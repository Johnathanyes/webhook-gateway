package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"webhook-gateway/internal/db"
	"webhook-gateway/internal/tunnel"
)

// dispatchTunnel delivers one event over an attached tunnel socket instead of
// HTTP. It returns the same attemptResult the HTTP path does, so the
// delivery status, attempt history, and trace view treat a tunnel exactly like
// any other destination — the ack's status code stands in for an HTTP response.
func (w *Worker) dispatchTunnel(ctx context.Context, dest db.Destination, eventID pgtype.UUID, deliveryID string) attemptResult {
	start := time.Now()

	conn, ok := w.tunnels.Lookup(dest.ID.Bytes)
	if !ok {
		// The socket is gone. Its destination row is deleted on disconnect, so
		// this is a narrow race (or a split-role deployment where the worker
		// runs in a different process than the one holding the socket). Either
		// way retrying cannot help.
		return attemptResult{
			errMsg:     text("tunnel not connected"),
			durationMs: elapsed(start),
			retryable:  false,
		}
	}

	event, err := w.q.GetEventForTunnelDelivery(ctx, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return attemptResult{errMsg: text("event gone"), durationMs: elapsed(start), retryable: false}
		}
		return attemptResult{errMsg: text(err.Error()), durationMs: elapsed(start), retryable: true}
	}

	// raw_headers is stored as the JSON form of http.Header, which is exactly
	// the shape the protocol's headers field takes.
	var headers map[string][]string
	if err := json.Unmarshal(event.RawHeaders, &headers); err != nil {
		return attemptResult{errMsg: text("decoding stored headers: " + err.Error()), durationMs: elapsed(start), retryable: false}
	}

	frame := tunnel.EventFrame{
		Type:        tunnel.FrameEvent,
		DeliveryID:  deliveryID,
		EventID:     uuidString(event.ID),
		Source:      event.SourceName,
		ContentType: event.ContentType.String,
		Headers:     headers,
		Body:        event.RawBody,
	}
	requestHeaders, _ := json.Marshal(headers)

	timeout := time.Duration(dest.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ackCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ack, err := conn.Deliver(ackCtx, frame)
	result := attemptResult{requestHeaders: requestHeaders, durationMs: elapsed(start)}
	if err != nil {
		result.errMsg = text(err.Error())
		// A closed tunnel is a destination that no longer exists; a timeout is
		// a client that never answered. Neither is worth another attempt at a
		// socket that is, by then, likely gone.
		result.retryable = false
		return result
	}

	if ack.Error != "" {
		// The client never reached its local target — no status code to record.
		result.errMsg = text(ack.Error)
		result.retryable = false
		return result
	}

	result.statusCode = pgtype.Int4{Int32: int32(ack.StatusCode), Valid: true}
	result.succeeded = ack.StatusCode >= 200 && ack.StatusCode < 300
	if !result.succeeded {
		result.errMsg = text(http.StatusText(ack.StatusCode))
		result.retryable = isRetryableStatus(ack.StatusCode)
	}
	return result
}

func elapsed(start time.Time) pgtype.Int4 {
	return pgtype.Int4{Int32: int32(time.Since(start).Milliseconds()), Valid: true}
}
