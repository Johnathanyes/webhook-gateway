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
	retryable       bool
}

// dispatch performs one HTTP POST of the event payload to the destination
func (w *Worker) dispatch(ctx context.Context, dest db.Destination, event db.GetEventForDeliveryRow, deliveryID string) attemptResult {
	timeout := time.Duration(dest.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, dest.Url, bytes.NewReader(event.RawBody))
	if err != nil {
		// A malformed destination URL won't fix itself; treat as terminal.
		return attemptResult{errMsg: text(err.Error()), retryable: false}
	}
	req.Header.Set("Webhook-Id", deliveryID)
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
		// No HTTP response: timeout, connection refused, DNS failure, etc. These
		// are transient by nature, so retry.
		result.errMsg = text(err.Error())
		result.retryable = true
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
		result.retryable = isRetryableStatus(resp.StatusCode)
	}
	return result
}

// Reports whether a non-2xx response is worth retrying. 408
// (Request Timeout) and 429 (Too Many Requests) are transient, as is any 5xx;
// every other 4xx is a client error the destination will keep rejecting.
func isRetryableStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
}

func text(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}
