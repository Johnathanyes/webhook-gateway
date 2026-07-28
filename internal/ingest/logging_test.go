package ingest_test

// Task #38 / BR-26: a signing secret and a webhook payload must never reach the
// logs. Both are attacker-relevant — the secret forges events, the payload is
// the customer's data — and the usual way they escape is an error path that
// interpolates whatever it was holding. So the failure paths matter more here
// than the happy one.
//
// This covers ingest, where the secret is decrypted and compared. The delivery
// worker, tunnel, and API are covered against the real binary's log by the
// grep in test/e2e.sh and test/e2e-tunnel.sh.

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"webhook-gateway/internal/db"
	"webhook-gateway/internal/ingest"
)

// Distinctive enough that a substring hit anywhere in the log output is
// unambiguous, and impossible to produce by coincidence.
const (
	markerSecret  = "whsec_MARKERSECRET_must_never_be_logged_9f3a"
	markerPayload = "MARKERPAYLOAD_must_never_be_logged_7c21"
)

// lockedBuffer collects log output written from any goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs redirects the default logger for the duration of the test. Debug
// level, because a secret logged at debug is still a secret in a log file.
func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buf
}

func TestIngestNeverLogsSecretsOrPayloads(t *testing.T) {
	pool := testDB(t)
	enc := testEncryptor(t)
	catalog := testCatalog(t)
	q := db.New(pool)

	secret := []byte(markerSecret)
	source := createGithubSource(t, pool, enc, secret)

	// Tight limits so the 413 and 429 rejection paths run too — both handle the
	// body, and both are places a well-meaning error message could echo it.
	mux := http.NewServeMux()
	ingest.Register(mux, pool, q, testRiverClient(t, pool), enc, catalog, ingest.Options{
		MaxBodyBytes:       512,
		RateLimitPerSecond: 2,
	})

	body := []byte(`{"action":"opened","marker":"` + markerPayload + `"}`)
	jsonHeaders := map[string]string{"Content-Type": "application/json"}

	logs := captureLogs(t)
	// A probe proves the capture is actually wired: without it, a test where
	// nothing logged at all would pass while asserting nothing.
	slog.Debug("log capture probe")

	// Correctly signed: the secret is decrypted and used.
	post(mux, source.EndpointPath, body, map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": githubSignature(secret, body),
	})

	// Signed with the wrong secret: the comparison fails, and a debug line that
	// printed "expected X got Y" would leak the real one.
	post(mux, source.EndpointPath, body, map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": githubSignature([]byte("wrong-secret"), body),
	})

	// No signature header at all.
	post(mux, source.EndpointPath, body, jsonHeaders)

	// Malformed signature header — a parse error is a classic place to echo input.
	post(mux, source.EndpointPath, body, map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": "sha256=not-hex-at-all",
	})

	// Over the body cap (413).
	oversized := append([]byte(`{"marker":"`+markerPayload+`","pad":"`), bytes.Repeat([]byte("x"), 1024)...)
	oversized = append(oversized, []byte(`"}`)...)
	post(mux, source.EndpointPath, oversized, jsonHeaders)

	// Past the rate limit (429).
	for range 10 {
		post(mux, source.EndpointPath, body, jsonHeaders)
	}

	// Unknown source path (404).
	post(mux, "src_does_not_exist", body, jsonHeaders)

	got := logs.String()
	if !strings.Contains(got, "log capture probe") {
		t.Fatal("log capture is not wired up; the assertions below would be vacuous")
	}
	if strings.Contains(got, markerSecret) {
		t.Errorf("signing secret leaked into the logs:\n%s", got)
	}
	if strings.Contains(got, markerPayload) {
		t.Errorf("payload contents leaked into the logs:\n%s", got)
	}
}
