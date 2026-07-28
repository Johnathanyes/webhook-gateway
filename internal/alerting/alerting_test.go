package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeNotifier records every alert it is handed, whether or not it then fails.
// Recording the attempt rather than the success is what lets the cooldown and
// retry tests tell "we tried and it failed" apart from "we never tried".
type fakeNotifier struct {
	mu     sync.Mutex
	alerts []Alert
	err    error
}

func (f *fakeNotifier) Notify(_ context.Context, a Alert) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts = append(f.alerts, a)
	return f.err
}

func (f *fakeNotifier) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// forCondition returns the recorded alerts for one destination+condition pair.
// Integration tests share a database, so assertions have to be scoped to the
// rows the test itself created.
func (f *fakeNotifier) forCondition(destinationID, condition string) []Alert {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Alert
	for _, a := range f.alerts {
		if a.DestinationID == destinationID && a.Condition == condition {
			out = append(out, a)
		}
	}
	return out
}

func TestAlertSubjectAndBody(t *testing.T) {
	a := Alert{
		Condition:       "dlq",
		DestinationID:   "019fa15e-0df2-7d15-9e8c-d33040462fc0",
		DestinationName: "billing-service",
		Detail:          "A delivery to \"billing-service\" dead-lettered.",
		FiredAt:         time.Date(2026, 7, 27, 4, 5, 6, 0, time.UTC),
	}

	subject := a.Subject()
	for _, want := range []string{"webhook-gateway", "dlq", "billing-service"} {
		if !strings.Contains(subject, want) {
			t.Errorf("Subject() = %q, want it to contain %q", subject, want)
		}
	}

	body := a.Body()
	for _, want := range []string{
		subject,
		a.DestinationID,
		a.Detail,
		"2026-07-27T04:05:06Z", // RFC3339 in UTC, so alerts read the same everywhere
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Body() = %q, want it to contain %q", body, want)
		}
	}
}

// One broken channel must not suppress the others — an operator who has both
// Slack and email configured should still get the email when Slack is down.
func TestMultiNotifierDeliversToEveryChannel(t *testing.T) {
	first := &fakeNotifier{}
	broken := &fakeNotifier{err: errors.New("slack is down")}
	last := &fakeNotifier{}

	err := multiNotifier{first, broken, last}.Notify(context.Background(), Alert{Condition: "dlq"})

	if err == nil {
		t.Fatal("Notify returned nil, want the broken channel's error")
	}
	if !strings.Contains(err.Error(), "slack is down") {
		t.Errorf("err = %v, want it to mention the failing channel", err)
	}
	for i, n := range []*fakeNotifier{first, broken, last} {
		if len(n.alerts) != 1 {
			t.Errorf("notifier %d received %d alerts, want 1", i, len(n.alerts))
		}
	}
}

func TestMultiNotifierReturnsNilWhenAllSucceed(t *testing.T) {
	if err := (multiNotifier{&fakeNotifier{}, &fakeNotifier{}}).Notify(context.Background(), Alert{}); err != nil {
		t.Errorf("Notify = %v, want nil", err)
	}
}

func TestBuildNotifier(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		channels int // 0 means buildNotifier must return nil
	}{
		{"nothing configured", Config{}, 0},
		{"slack present but empty url", Config{Slack: &SlackConfig{}}, 0},
		{"smtp present but empty host", Config{SMTP: &SMTPConfig{}}, 0},
		{"slack only", Config{Slack: &SlackConfig{WebhookURL: "https://hooks.example/x"}}, 1},
		{"smtp only", Config{SMTP: &SMTPConfig{Host: "mail:25"}}, 1},
		{
			"both",
			Config{
				Slack: &SlackConfig{WebhookURL: "https://hooks.example/x"},
				SMTP:  &SMTPConfig{Host: "mail:25"},
			},
			2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildNotifier(tt.cfg)
			if tt.channels == 0 {
				if got != nil {
					t.Errorf("buildNotifier = %#v, want nil when no channel is usable", got)
				}
				return
			}
			multi, ok := got.(multiNotifier)
			if !ok {
				t.Fatalf("buildNotifier returned %T, want multiNotifier", got)
			}
			if len(multi) != tt.channels {
				t.Errorf("channels = %d, want %d", len(multi), tt.channels)
			}
		})
	}
}

func TestConfigApplyDefaults(t *testing.T) {
	// Zero and negative both mean "unset" and fall back.
	for _, c := range []Config{{}, {CooldownMinutes: -1, WindowMinutes: -1, MinDeliveries: -1}} {
		cfg := c
		cfg.applyDefaults()
		if cfg.CooldownMinutes != 60 || cfg.WindowMinutes != 15 || cfg.MinDeliveries != 20 {
			t.Errorf("defaults = %d/%d/%d, want 60/15/20",
				cfg.CooldownMinutes, cfg.WindowMinutes, cfg.MinDeliveries)
		}
	}

	explicit := Config{CooldownMinutes: 5, WindowMinutes: 30, MinDeliveries: 3}
	explicit.applyDefaults()
	if explicit.CooldownMinutes != 5 || explicit.WindowMinutes != 30 || explicit.MinDeliveries != 3 {
		t.Errorf("applyDefaults overwrote explicit values: %+v", explicit)
	}

	// FailureThreshold has no default: 0 disables the condition entirely.
	if explicit.FailureThreshold != 0 {
		t.Errorf("FailureThreshold = %v, want 0 (condition disabled)", explicit.FailureThreshold)
	}
}

// With nothing configured to notify, conditions would fire into the void — so
// Run returns before it queries anything. The nil *db.Queries proves it: any
// query would panic.
func TestEvaluatorWithoutNotifierQueriesNothing(t *testing.T) {
	if err := NewEvaluator(nil, Config{WindowMinutes: 15}, nil).Run(context.Background()); err != nil {
		t.Errorf("Run = %v, want nil", err)
	}
}

func TestSlackNotifierPostsAlertBody(t *testing.T) {
	type received struct {
		method      string
		contentType string
		payload     map[string]string
	}
	got := make(chan received, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]string
		_ = json.Unmarshal(body, &payload)
		got <- received{method: r.Method, contentType: r.Header.Get("Content-Type"), payload: payload}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	alert := Alert{Condition: "failure_rate", DestinationName: "billing", Detail: "Failure rate 80%", FiredAt: time.Now()}
	if err := newSlackNotifier(srv.URL).Notify(context.Background(), alert); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	r := <-got
	if r.method != http.MethodPost {
		t.Errorf("method = %s, want POST", r.method)
	}
	if r.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", r.contentType)
	}
	if !strings.Contains(r.payload["text"], "Failure rate 80%") {
		t.Errorf("text = %q, want it to carry the alert detail", r.payload["text"])
	}
}

// A webhook URL that answers non-2xx is a failed notification, not a silent
// success — the evaluator relies on this to leave the cooldown unrecorded.
func TestSlackNotifierFailsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := newSlackNotifier(srv.URL).Notify(context.Background(), Alert{})
	if err == nil {
		t.Fatal("Notify = nil, want an error for a 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want it to name the status code", err)
	}
}

func TestSlackNotifierFailsWhenUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening any more

	if err := newSlackNotifier(url).Notify(context.Background(), Alert{}); err == nil {
		t.Fatal("Notify = nil, want a transport error")
	}
}

func TestNewSMTPNotifierWiring(t *testing.T) {
	cfg := SMTPConfig{Host: "mail.example.com:587", From: "alerts@example.com", To: []string{"ops@example.com"}}

	// No credentials means an unauthenticated relay, not empty credentials.
	if got := newSMTPNotifier(cfg); got.Auth != nil {
		t.Error("Auth is set for a relay with no username")
	}

	cfg.Username = "user"
	cfg.Password = "pass"
	authed := newSMTPNotifier(cfg)
	if authed.Auth == nil {
		t.Error("Auth is nil despite a configured username")
	}
	if authed.Addr != "mail.example.com:587" {
		t.Errorf("Addr = %q, want the host:port from config", authed.Addr)
	}
	if authed.From != "alerts@example.com" || len(authed.To) != 1 {
		t.Errorf("From/To not wired through: %+v", authed)
	}
}
