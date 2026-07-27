// Package tunnel is the client half of the gateway's local-development tunnel.
package tunnel

// Frame type discriminators, carried in every frame's "type" field.
const (
	FrameReady = "ready"
	FrameEvent = "event"
	FrameAck   = "ack"
)

// typedFrame peeks at a frame's discriminator before decoding it fully, so an
// unrecognized type can be skipped rather than mis-parsed.
type typedFrame struct {
	Type string `json:"type"`
}

// ReadyFrame is the server's first frame, confirming what this tunnel receives.
type ReadyFrame struct {
	Type          string   `json:"type"`
	DestinationID string   `json:"destination_id"`
	Sources       []string `json:"sources"`
}

// EventFrame is one delivery pushed down the socket. Body is base64 on the
// wire; encoding/json decodes it back into raw bytes here.
type EventFrame struct {
	Type        string              `json:"type"`
	DeliveryID  string              `json:"delivery_id"`
	EventID     string              `json:"event_id"`
	Source      string              `json:"source"`
	ContentType string              `json:"content_type,omitempty"`
	Headers     map[string][]string `json:"headers"`
	Body        []byte              `json:"body"`
}

// AckFrame reports what the local handler did with an event. Exactly one of
// StatusCode or Error is meaningful: Error means the local target was never
// reached, so there is no status to report.
type AckFrame struct {
	Type       string `json:"type"`
	DeliveryID string `json:"delivery_id"`
	StatusCode int    `json:"status_code"`
	Error      string `json:"error,omitempty"`
	DurationMs int    `json:"duration_ms,omitempty"`
}
