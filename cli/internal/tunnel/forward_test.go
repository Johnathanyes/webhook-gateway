package tunnel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The point of the tunnel is that a local handler sees what the provider sent,
// so the body must be byte-exact and provider headers must survive.
func TestForwardReplaysOriginalRequest(t *testing.T) {
	var (
		gotBody      []byte
		gotSignature string
		gotWebhookID string
		gotMethod    string
	)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotSignature = r.Header.Get("Stripe-Signature")
		gotWebhookID = r.Header.Get("Webhook-Id")
		gotMethod = r.Method
		w.WriteHeader(http.StatusAccepted)
	}))
	defer sink.Close()

	body := []byte(`{"id":"evt_1","type":"payment_intent.succeeded"}`)
	frame := EventFrame{
		DeliveryID: "del-123",
		Source:     "stripe",
		Headers: map[string][]string{
			"Content-Type":     {"application/json"},
			"Stripe-Signature": {"t=1,v1=abc"},
		},
		Body: body,
	}

	status, _, err := forward(context.Background(), sink.Client(), sink.URL, frame)
	if err != nil {
		t.Fatalf("forward: %v", err)
	}

	if status != http.StatusAccepted {
		t.Errorf("status = %d, want 202", status)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if string(gotBody) != string(body) {
		t.Errorf("body = %q, want %q", gotBody, body)
	}
	if gotSignature != "t=1,v1=abc" {
		t.Errorf("Stripe-Signature = %q, want it preserved", gotSignature)
	}
	// Parity with HTTP dispatch, which sets Webhook-Id on every delivery.
	if gotWebhookID != "del-123" {
		t.Errorf("Webhook-Id = %q, want del-123", gotWebhookID)
	}
}

// Replaying the provider's Content-Length or Host onto a request to localhost
// would be wrong (and net/http rejects a bad Content-Length outright).
func TestForwardStripsHopByHopHeaders(t *testing.T) {
	var gotLength, gotHost, gotConnection string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLength = r.Header.Get("Content-Length")
		gotHost = r.Host
		gotConnection = r.Header.Get("Connection")
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()

	frame := EventFrame{
		DeliveryID: "del-1",
		Headers: map[string][]string{
			"Content-Length":    {"99999"},
			"Host":              {"api.stripe.com"},
			"Connection":        {"keep-alive"},
			"Transfer-Encoding": {"chunked"},
		},
		Body: []byte(`{}`),
	}

	if _, _, err := forward(context.Background(), sink.Client(), sink.URL, frame); err != nil {
		t.Fatalf("forward: %v", err)
	}

	if gotLength == "99999" {
		t.Error("stale Content-Length was forwarded")
	}
	if gotHost == "api.stripe.com" {
		t.Error("provider Host header overrode the local target")
	}
	if gotConnection == "keep-alive" {
		t.Error("hop-by-hop Connection header was forwarded")
	}
}

func TestForwardReportsUnreachableTarget(t *testing.T) {
	client := &http.Client{Timeout: 2 * time.Second}
	// Port 1 on loopback refuses connections.
	_, _, err := forward(context.Background(), client, "http://127.0.0.1:1", EventFrame{Body: []byte(`{}`)})
	if err == nil {
		t.Fatal("forward to a closed port returned no error")
	}
}

func TestEventType(t *testing.T) {
	tests := []struct {
		name  string
		frame EventFrame
		want  string
	}{
		{
			name:  "stripe uses the body's type field",
			frame: EventFrame{Body: []byte(`{"type":"payment_intent.succeeded"}`)},
			want:  "payment_intent.succeeded",
		},
		{
			name: "github uses a header",
			frame: EventFrame{
				Headers: map[string][]string{"X-Github-Event": {"pull_request"}},
				Body:    []byte(`{"action":"opened"}`),
			},
			want: "pull_request",
		},
		{
			name:  "event_type is honored",
			frame: EventFrame{Body: []byte(`{"event_type":"invoice.paid"}`)},
			want:  "invoice.paid",
		},
		{
			name:  "unknown shape yields no label",
			frame: EventFrame{Body: []byte(`{"id":"1"}`)},
			want:  "",
		},
		{
			name:  "non-JSON body yields no label",
			frame: EventFrame{Body: []byte(`<xml/>`)},
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := eventType(tt.frame); got != tt.want {
				t.Errorf("eventType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWebsocketURL(t *testing.T) {
	tests := []struct {
		gateway, source, want string
		wantErr               bool
	}{
		{gateway: "http://localhost:8080", source: "stripe", want: "ws://localhost:8080/api/tunnel?source=stripe"},
		{gateway: "https://gw.example.com", source: "", want: "wss://gw.example.com/api/tunnel"},
		{gateway: "https://example.com/gw/", source: "", want: "wss://example.com/gw/api/tunnel"},
		{gateway: "ftp://example.com", wantErr: true},
	}
	for _, tt := range tests {
		got, err := websocketURL(tt.gateway, tt.source)
		if tt.wantErr {
			if err == nil {
				t.Errorf("websocketURL(%q) = %q, want error", tt.gateway, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("websocketURL(%q): %v", tt.gateway, err)
			continue
		}
		if got != tt.want {
			t.Errorf("websocketURL(%q, %q) = %q, want %q", tt.gateway, tt.source, got, tt.want)
		}
	}
}
