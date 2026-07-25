// Package tunnel implements the gateway half of the local-development tunnel
// a CLI opens a WebSocket, the gateway registers it as an ephemeral
// destination, and matching events are pushed down the socket instead of being
// POSTed over HTTP.
//
// The wire contract lives in docs/tunnel-protocol.md; the types below are its
// normative definition.
package tunnel

import "strings"

// URLScheme marks a destination row as tunnel-backed. The delivery worker keys
// off this prefix to route through the registry instead of net/http.
const URLScheme = "tunnel://"

// IsURL reports whether a destination URL belongs to a tunnel.
func IsURL(destURL string) bool { return strings.HasPrefix(destURL, URLScheme) }

// Frame type discriminators, carried in every frame's "type" field.
const (
	FrameReady = "ready"
	FrameEvent = "event"
	FrameAck   = "ack"
)

// ReadyFrame is the first frame the server sends after a successful upgrade. It
// tells the client which ephemeral destination it owns and which sources are
// routed to it, so the CLI can print a useful banner.
type ReadyFrame struct {
	Type          string   `json:"type"`
	DestinationID string   `json:"destination_id"`
	Sources       []string `json:"sources"`
}

// EventFrame carries one delivery to the client. Body is the provider's
// verbatim request body; encoding/json renders []byte as base64, which keeps
// non-UTF-8 payloads (raw XML, binary) byte-exact over a text frame.
//
// Headers are the provider's original request headers. This is a deliberate
// divergence from HTTP dispatch, which forwards none of them: a tunnel exists
// so a local handler can be exercised exactly as production would be,
// signature headers included.
type EventFrame struct {
	Type        string              `json:"type"`
	DeliveryID  string              `json:"delivery_id"`
	EventID     string              `json:"event_id"`
	Source      string              `json:"source"`
	ContentType string              `json:"content_type,omitempty"`
	Headers     map[string][]string `json:"headers"`
	Body        []byte              `json:"body"`
}

// AckFrame is the client's report of what its local handler did with an event.
// StatusCode is recorded on the delivery attempt exactly as an HTTP response
// status would be, so 2xx succeeds and anything else fails. Error is set when
// the client never reached its local target at all.
type AckFrame struct {
	Type       string `json:"type"`
	DeliveryID string `json:"delivery_id"`
	StatusCode int    `json:"status_code"`
	Error      string `json:"error,omitempty"`
	DurationMs int    `json:"duration_ms,omitempty"`
}
