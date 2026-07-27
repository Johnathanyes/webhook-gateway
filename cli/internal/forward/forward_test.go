package forward

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The whole point of replaying a webhook locally is that the handler sees what
// the provider sent — byte-exact body, signature headers intact.
func TestPostReplaysOriginalRequest(t *testing.T) {
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
	res, err := Post(context.Background(), sink.Client(), Request{
		Target: sink.URL,
		Headers: map[string][]string{
			"Content-Type":     {"application/json"},
			"Stripe-Signature": {"t=1,v1=abc"},
		},
		Body:      body,
		WebhookID: "del-123",
	})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}

	if res.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want 202", res.StatusCode)
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
	if gotWebhookID != "del-123" {
		t.Errorf("Webhook-Id = %q, want del-123", gotWebhookID)
	}
}

// Replay has no delivery id to report, so it must not invent one.
func TestPostOmitsEmptyWebhookID(t *testing.T) {
	var present bool
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header["Webhook-Id"]
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()

	if _, err := Post(context.Background(), sink.Client(), Request{
		Target: sink.URL,
		Body:   []byte(`{}`),
	}); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if present {
		t.Error("Webhook-Id was sent despite an empty WebhookID")
	}
}

// Replaying the provider's Content-Length or Host onto a localhost request
// would be wrong (and net/http rejects a bad Content-Length outright).
func TestPostStripsHopByHopHeaders(t *testing.T) {
	var gotLength, gotHost, gotConnection string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLength = r.Header.Get("Content-Length")
		gotHost = r.Host
		gotConnection = r.Header.Get("Connection")
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()

	_, err := Post(context.Background(), sink.Client(), Request{
		Target: sink.URL,
		Headers: map[string][]string{
			"Content-Length":    {"99999"},
			"Host":              {"api.stripe.com"},
			"Connection":        {"keep-alive"},
			"Transfer-Encoding": {"chunked"},
		},
		Body: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("Post: %v", err)
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

func TestPostReportsUnreachableTarget(t *testing.T) {
	client := &http.Client{Timeout: 2 * time.Second}
	// Port 1 on loopback refuses connections.
	_, err := Post(context.Background(), client, Request{Target: "http://127.0.0.1:1", Body: []byte(`{}`)})
	if err == nil {
		t.Fatal("Post to a closed port returned no error")
	}
}

func TestEventType(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string][]string
		body    string
		want    string
	}{
		{name: "stripe uses the body's type field", body: `{"type":"payment_intent.succeeded"}`, want: "payment_intent.succeeded"},
		{name: "github uses a header", headers: map[string][]string{"X-Github-Event": {"pull_request"}}, body: `{"action":"opened"}`, want: "pull_request"},
		{name: "event_type is honored", body: `{"event_type":"invoice.paid"}`, want: "invoice.paid"},
		{name: "unknown shape yields no label", body: `{"id":"1"}`, want: ""},
		{name: "non-JSON body yields no label", body: `<xml/>`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EventType(tt.headers, []byte(tt.body)); got != tt.want {
				t.Errorf("EventType() = %q, want %q", got, tt.want)
			}
		})
	}
}

// `listen` and `replay` must print identically, so the format lives here.
func TestLine(t *testing.T) {
	res := Result{StatusCode: 200, Elapsed: 45 * time.Millisecond}

	got := Line("stripe", "payment_intent.succeeded", res, nil)
	if !strings.HasSuffix(got, "stripe payment_intent.succeeded → 200 (45ms)") {
		t.Errorf("Line() = %q, want it to end with the documented format", got)
	}

	if got := Line("stripe", "", res, nil); !strings.HasSuffix(got, "stripe → 200 (45ms)") {
		t.Errorf("Line() without an event type = %q", got)
	}

	failed := Line("stripe", "", Result{Elapsed: time.Millisecond}, errors.New("connection refused"))
	if !strings.Contains(failed, "→ error: connection refused") {
		t.Errorf("Line() with an error = %q", failed)
	}
}
