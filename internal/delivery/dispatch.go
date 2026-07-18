package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"webhook-gateway/internal/db"
)

// maxResponseBodyBytes caps how much of a destination's response we keep for the
// trace view, so one chatty endpoint can't bloat delivery_attempts.
const maxResponseBodyBytes = 64 * 1024

// attemptResult is the outcome of a single HTTP try, shaped for both the
// delivery_attempts row and the delivery status decision.
type attemptResult struct {
	requestHeaders  []byte
	statusCode      pgtype.Int4
	responseHeaders []byte
	responseBody    pgtype.Text
	errMsg          pgtype.Text
	durationMs      pgtype.Int4
	succeeded       bool
}

// dispatch performs one HTTP POST of the event payload to the destination,
// bounded by the destination's timeout.
func (w *Worker) dispatch(ctx context.Context, dest db.Destination, event db.GetEventForDeliveryRow) attemptResult {
	timeout := time.Duration(dest.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, dest.Url, bytes.NewReader(event.RawBody))
	if err != nil {
		return attemptResult{errMsg: text(err.Error())}
	}
	if event.ContentType.Valid {
		req.Header.Set("Content-Type", event.ContentType.String)
	}
	reqHeaders, _ := json.Marshal(req.Header)

	start := time.Now()
	resp, err := w.httpClient.Do(req)
	duration := time.Since(start)

	result := attemptResult{
		requestHeaders: reqHeaders,
		durationMs:     pgtype.Int4{Int32: int32(duration.Milliseconds()), Valid: true},
	}
	if err != nil {
		// No HTTP response: timeout, connection refused, DNS failure, etc.
		result.errMsg = text(err.Error())
		return result
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
	respHeaders, _ := json.Marshal(resp.Header)

	result.statusCode = pgtype.Int4{Int32: int32(resp.StatusCode), Valid: true}
	result.responseHeaders = respHeaders
	if len(body) > 0 {
		result.responseBody = text(string(body))
	}
	result.succeeded = resp.StatusCode >= 200 && resp.StatusCode < 300
	if !result.succeeded {
		result.errMsg = text(http.StatusText(resp.StatusCode))
	}
	return result
}

func text(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}
