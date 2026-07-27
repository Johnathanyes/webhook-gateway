// Package forward replays a stored webhook at a local HTTP server. Both
// `listen` (live, over the tunnel) and `replay` (after the fact, from the
// events API) deliver through it, so "what a local handler sees" is defined in
// exactly one place.
package forward

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// Request is one webhook to replay locally.
type Request struct {
	Target    string              // local URL to POST to
	Headers   map[string][]string // the provider's original headers
	Body      []byte              // the provider's original body, verbatim
	WebhookID string              // sets Webhook-Id; omitted when empty
}

// Result is what the local handler said.
type Result struct {
	StatusCode int
	Elapsed    time.Duration
}

// Post reproduces the provider's request against a local server: same body
// bytes, same headers, and the Webhook-Id the gateway's HTTP dispatch sets.
// Preserving the signature headers is the point — a local handler must be able
// to verify them exactly as it would in production.
func Post(ctx context.Context, httpClient *http.Client, req Request) (Result, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.Target, bytes.NewReader(req.Body))
	if err != nil {
		return Result{}, err
	}

	for name, values := range req.Headers {
		if hopByHopHeaders[http.CanonicalHeaderKey(name)] {
			continue
		}
		for _, v := range values {
			httpReq.Header.Add(name, v)
		}
	}
	if req.WebhookID != "" {
		httpReq.Header.Set("Webhook-Id", req.WebhookID)
	}

	start := time.Now()
	resp, err := httpClient.Do(httpReq)
	elapsed := time.Since(start)
	if err != nil {
		return Result{Elapsed: elapsed}, err
	}
	defer func() { _ = resp.Body.Close() }()

	// The body is drained and discarded: only the status is reported onward,
	// and draining is what lets the keep-alive connection be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	return Result{StatusCode: resp.StatusCode, Elapsed: elapsed}, nil
}

// EventType is a best-effort label for the log line — "payment_intent.succeeded"
// rather than just "stripe". The tunnel protocol deliberately doesn't carry it:
// where an event's type lives is provider-specific, and this is a display
// nicety, so it is sniffed here rather than added to the wire format.
func EventType(headers map[string][]string, body []byte) string {
	// GitHub puts it in a header; Stripe and most JSON APIs put it in the body.
	if v := http.Header(headers).Get("X-GitHub-Event"); v != "" {
		return v
	}

	var parsed struct {
		Type      string `json:"type"`
		EventType string `json:"event_type"`
		Event     string `json:"event"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	for _, candidate := range []string{parsed.Type, parsed.EventType, parsed.Event} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

// Line renders the one-line-per-event summary:
//
//	(ex) 12:04:31 stripe payment_intent.succeeded → 200 (45ms)
//
// Shared so `listen` and `replay` read identically in a terminal.
func Line(source, eventType string, res Result, err error) string {
	label := source
	if eventType != "" {
		label += " " + eventType
	}
	stamp := time.Now().Format("15:04:05")
	elapsed := res.Elapsed.Round(time.Millisecond)
	if err != nil {
		return fmt.Sprintf("%s %s → error: %v (%s)", stamp, label, err, elapsed)
	}
	return fmt.Sprintf("%s %s → %d (%s)", stamp, label, res.StatusCode, elapsed)
}
