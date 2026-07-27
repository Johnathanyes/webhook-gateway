package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webhook-gateway-cli/internal/config"
)

// fakeGateway serves GET /api/sources, the endpoint login round-trips against.
func fakeGateway(t *testing.T, status int, body string) (*httptest.Server, *string) {
	t.Helper()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &gotAuth
}

// run executes the command tree with piped stdin and captured output.
func run(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root := NewRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// Done-test: login authenticates against a gateway and persists
// credentials that later commands can load.
func TestLoginStoresVerifiedCredentials(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv, gotAuth := fakeGateway(t, http.StatusOK,
		`[{"id":"a","name":"stripe","provider_type":"stripe","endpoint_path":"src_1","created_at":"2026-07-25T00:00:00Z"}]`)

	out, err := run(t, "", "login", "--url", srv.URL, "--api-key", "whg_key")
	if err != nil {
		t.Fatalf("login: %v (output: %s)", err, out)
	}

	if *gotAuth != "Bearer whg_key" {
		t.Errorf("gateway saw Authorization %q, want %q", *gotAuth, "Bearer whg_key")
	}
	if !strings.Contains(out, "1 source configured") {
		t.Errorf("output does not report the source count:\n%s", out)
	}

	stored, err := config.Load()
	if err != nil {
		t.Fatalf("loading saved config: %v", err)
	}
	if stored.GatewayURL != srv.URL || stored.APIKey != "whg_key" {
		t.Errorf("stored config = %+v, want url=%s key=whg_key", stored, srv.URL)
	}
}

// Prompts are the interactive path; piped stdin exercises the same code.
func TestLoginPromptsForURLAndKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv, gotAuth := fakeGateway(t, http.StatusOK, `[]`)

	out, err := run(t, srv.URL+"\nwhg_prompted\n", "login")
	if err != nil {
		t.Fatalf("login: %v (output: %s)", err, out)
	}

	if *gotAuth != "Bearer whg_prompted" {
		t.Errorf("gateway saw Authorization %q, want %q", *gotAuth, "Bearer whg_prompted")
	}
	if !strings.Contains(out, "Gateway URL [") || !strings.Contains(out, "API key:") {
		t.Errorf("expected both prompts in output:\n%s", out)
	}
	if !strings.Contains(out, "No sources yet") {
		t.Errorf("empty gateway should hint at creating a source:\n%s", out)
	}
}

// A credential that doesn't work must not be written to disk — otherwise the
// failure resurfaces later, further from its cause.
func TestLoginRejectsBadCredentialWithoutSaving(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv, _ := fakeGateway(t, http.StatusUnauthorized, `{"error":"unauthorized"}`)

	_, err := run(t, "", "login", "--url", srv.URL, "--api-key", "whg_wrong")
	if err == nil {
		t.Fatal("login succeeded with a rejected credential")
	}
	if !strings.Contains(err.Error(), "rejected the credential") {
		t.Errorf("error = %v, want it to mention the rejected credential", err)
	}
	if _, err := config.Load(); err == nil {
		t.Error("config was saved despite failed authentication")
	}
}

// A key missing the `read` scope is a different fix from a wrong key.
func TestLoginReportsMissingScope(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv, _ := fakeGateway(t, http.StatusForbidden, `{"error":"missing scope: read"}`)

	_, err := run(t, "", "login", "--url", srv.URL, "--api-key", "whg_noscope")
	if err == nil {
		t.Fatal("login succeeded with an unscoped key")
	}
	if !strings.Contains(err.Error(), "`read` scope") {
		t.Errorf("error = %v, want it to name the missing scope", err)
	}
}

func TestVersionCommand(t *testing.T) {
	out, err := run(t, "", "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.HasPrefix(out, "whg dev ") {
		t.Errorf("version output = %q, want it to start with %q", out, "whg dev ")
	}
}
