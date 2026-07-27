package cli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"webhook-gateway-cli/internal/config"
)

// loginTo stores credentials for a fake gateway so command tests can run
// against it the way a real session would.
func loginTo(t *testing.T, gatewayURL string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := config.Save(config.Config{GatewayURL: gatewayURL, APIKey: "whg_test"}); err != nil {
		t.Fatalf("seeding config: %v", err)
	}
}

const sourcesJSON = `[{"id":"src-1","name":"stripe","provider_type":"stripe","endpoint_path":"src_1","created_at":"2026-07-25T00:00:00Z"}]`

// eventJSON renders the gateway's event-detail shape. raw_body is base64 on the
// wire, exactly as encoding/json renders a []byte server-side.
func eventJSON(id, body string) string {
	return fmt.Sprintf(`{
		"id": %q,
		"source_id": "src-1",
		"raw_headers": {"Content-Type":["application/json"],"Stripe-Signature":["t=1,v1=abc"]},
		"raw_body": %q,
		"content_type": "application/json",
		"verified": true,
		"received_at": "2026-07-25T00:00:00Z"
	}`, id, base64.StdEncoding.EncodeToString([]byte(body)))
}

// Done-test: a stored event is replayed into a local sink with a
// byte-equal body and its provider headers preserved.
func TestReplayByEventID(t *testing.T) {
	const body = `{"id":"evt_1","type":"payment_intent.succeeded"}`

	var gotBody, gotSignature string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody, gotSignature = string(raw), r.Header.Get("Stripe-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/sources":
			_, _ = w.Write([]byte(sourcesJSON))
		case r.URL.Path == "/api/events/evt-abc":
			_, _ = w.Write([]byte(eventJSON("evt-abc", body)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gateway.Close()
	loginTo(t, gateway.URL)

	out, err := run(t, "", "replay", "evt-abc", "--forward-to", sink.URL)
	if err != nil {
		t.Fatalf("replay: %v (output: %s)", err, out)
	}

	if gotBody != body {
		t.Errorf("sink body = %q, want %q", gotBody, body)
	}
	if gotSignature != "t=1,v1=abc" {
		t.Errorf("Stripe-Signature = %q, want it preserved", gotSignature)
	}
	if !strings.Contains(out, "stripe payment_intent.succeeded → 200") {
		t.Errorf("output missing the event line:\n%s", out)
	}
}

// --last N --source <name> resolves the name, lists that source's events, and
// replays each one.
func TestReplayLastNForSource(t *testing.T) {
	var mu sync.Mutex
	var delivered []string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		delivered = append(delivered, string(raw))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()

	var gotSourceFilter, gotLimit string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sources":
			_, _ = w.Write([]byte(sourcesJSON))
		case "/api/events":
			gotSourceFilter = r.URL.Query().Get("source_id")
			gotLimit = r.URL.Query().Get("limit")
			_, _ = w.Write([]byte(`{"events":[
				{"id":"evt-1","source_id":"src-1","verified":true,"received_at":"2026-07-25T00:00:02Z"},
				{"id":"evt-2","source_id":"src-1","verified":true,"received_at":"2026-07-25T00:00:01Z"}
			]}`))
		case "/api/events/evt-1":
			_, _ = w.Write([]byte(eventJSON("evt-1", `{"n":1}`)))
		case "/api/events/evt-2":
			_, _ = w.Write([]byte(eventJSON("evt-2", `{"n":2}`)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gateway.Close()
	loginTo(t, gateway.URL)

	out, err := run(t, "", "replay", "--last", "2", "--source", "stripe", "--forward-to", sink.URL)
	if err != nil {
		t.Fatalf("replay: %v (output: %s)", err, out)
	}

	if gotSourceFilter != "src-1" {
		t.Errorf("source_id filter = %q, want src-1 (name should resolve to id)", gotSourceFilter)
	}
	if gotLimit != "2" {
		t.Errorf("limit = %q, want 2", gotLimit)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 2 {
		t.Fatalf("delivered %d events, want 2: %v", len(delivered), delivered)
	}
	// Newest first, matching the list endpoint's ordering.
	if delivered[0] != `{"n":1}` || delivered[1] != `{"n":2}` {
		t.Errorf("delivered = %v, want newest-first order", delivered)
	}
}

func TestReplayRejectsAmbiguousArguments(t *testing.T) {
	loginTo(t, "http://localhost:1")

	if _, err := run(t, "", "replay", "--forward-to", "localhost:3000"); err == nil {
		t.Error("replay with neither an id nor --last should fail")
	}
	if _, err := run(t, "", "replay", "evt-1", "--last", "3", "--forward-to", "localhost:3000"); err == nil {
		t.Error("replay with both an id and --last should fail")
	}
}

// A local server that isn't running is the common case; the command must
// report it as a failure rather than exiting 0.
func TestReplayFailsWhenLocalTargetIsDown(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sources":
			_, _ = w.Write([]byte(sourcesJSON))
		case "/api/events/evt-abc":
			_, _ = w.Write([]byte(eventJSON("evt-abc", `{}`)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gateway.Close()
	loginTo(t, gateway.URL)

	_, err := run(t, "", "replay", "evt-abc", "--forward-to", "http://127.0.0.1:1")
	if err == nil {
		t.Fatal("replay to a closed port returned no error")
	}
}

// Done-test at command level: trigger resolves the source by name and
// calls the gateway's test-event endpoint.
func TestTriggerSendsSampleEvent(t *testing.T) {
	var gotPath, gotMethod string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/sources" {
			_, _ = w.Write([]byte(sourcesJSON))
			return
		}
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewEncoder(w).Encode(map[string]any{"event_id": "evt-new", "verified": true})
	}))
	defer gateway.Close()
	loginTo(t, gateway.URL)

	out, err := run(t, "", "trigger", "--source", "stripe")
	if err != nil {
		t.Fatalf("trigger: %v (output: %s)", err, out)
	}

	if gotPath != "/api/sources/src-1/test-event" {
		t.Errorf("path = %q, want /api/sources/src-1/test-event", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if !strings.Contains(out, "evt-new") || !strings.Contains(out, "verified: true") {
		t.Errorf("output should report the new event and its verification:\n%s", out)
	}
}

func TestTriggerUnknownSource(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sourcesJSON))
	}))
	defer gateway.Close()
	loginTo(t, gateway.URL)

	_, err := run(t, "", "trigger", "--source", "nope")
	if err == nil {
		t.Fatal("trigger with an unknown source returned no error")
	}
	if !strings.Contains(err.Error(), `no source named "nope"`) {
		t.Errorf("error = %v, want it to name the missing source", err)
	}
}

// trigger needs `write`, not `read` — the message must say which.
func TestTriggerReportsMissingWriteScope(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/sources" {
			_, _ = w.Write([]byte(sourcesJSON))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"missing scope: write"}`))
	}))
	defer gateway.Close()
	loginTo(t, gateway.URL)

	_, err := run(t, "", "trigger", "--source", "stripe")
	if err == nil {
		t.Fatal("trigger with an unscoped key returned no error")
	}
	if !strings.Contains(err.Error(), "`write` scope") {
		t.Errorf("error = %v, want it to name the missing scope", err)
	}
}

// Commands that need credentials must say so rather than failing obscurely.
func TestCommandsRequireLogin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	for _, args := range [][]string{
		{"trigger", "--source", "stripe"},
		{"replay", "evt-1", "--forward-to", "localhost:3000"},
		{"listen", "--forward-to", "localhost:3000"},
	} {
		_, err := run(t, "", args...)
		if err == nil {
			t.Errorf("%v without credentials returned no error", args)
			continue
		}
		if !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("%v error = %v, want a not-logged-in message", args, err)
		}
	}
}
