package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// hopByHopHeaders are connection-scoped and must not be replayed onto a new
// request. Host and Content-Length are excluded too: net/http
// derives both from the request it is actually sending, and a stale value from
// the provider's original request would be wrong.
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Content-Length":      true,
	"Host":                true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

// forward POSTs one event to the developer's local handler, reproducing the
// provider's original request as closely as possible: same body bytes, same
// headers, and the Webhook-Id the gateway's HTTP dispatch would have set.
func forward(ctx context.Context, httpClient *http.Client, target string, frame EventFrame) (int, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(frame.Body))
	if err != nil {
		return 0, 0, err
	}

	for name, values := range frame.Headers {
		if hopByHopHeaders[http.CanonicalHeaderKey(name)] {
			continue
		}
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	req.Header.Set("Webhook-Id", frame.DeliveryID)

	start := time.Now()
	resp, err := httpClient.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return 0, elapsed, err
	}
	defer func() { _ = resp.Body.Close() }()

	// The body is drained and discarded: the gateway records the ack's status,
	// not the local handler's response body, and draining is what lets the
	// keep-alive connection be reused for the next event.
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, elapsed, nil
}

// eventType is a best-effort label for the log line.
// The tunnel protocol deliberately doesn't carry it:
// where an event's type lives is provider-specific, and this is a display
// nicety, so it is sniffed here rather than added to the wire format.
func eventType(frame EventFrame) string {
	// GitHub puts it in a header; Stripe and most JSON APIs put it in the body.
	if v := http.Header(frame.Headers).Get("X-GitHub-Event"); v != "" {
		return v
	}

	var body struct {
		Type      string `json:"type"`
		EventType string `json:"event_type"`
		Event     string `json:"event"`
	}
	if err := json.Unmarshal(frame.Body, &body); err != nil {
		return ""
	}
	for _, candidate := range []string{body.Type, body.EventType, body.Event} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}
