package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListSourcesSendsBearerToken(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"abc","name":"stripe","provider_type":"stripe","endpoint_path":"src_1","created_at":"2026-07-25T00:00:00Z"}]`))
	}))
	defer srv.Close()

	sources, err := New(srv.URL, "whg_test").ListSources(context.Background())
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}

	if gotAuth != "Bearer whg_test" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer whg_test")
	}
	if gotPath != "/api/sources" {
		t.Errorf("path = %q, want /api/sources", gotPath)
	}
	if len(sources) != 1 || sources[0].Name != "stripe" {
		t.Fatalf("sources = %+v, want one named stripe", sources)
	}
}

// The gateway reports every failure through the {"error": "..."} envelope, so
// the message must survive into the CLI's output.
func TestErrorEnvelopeIsSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"missing scope: read"}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "whg_test").ListSources(context.Background())

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T), want *APIError", err, err)
	}
	if apiErr.Status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", apiErr.Status)
	}
	if apiErr.Message != "missing scope: read" {
		t.Errorf("message = %q, want %q", apiErr.Message, "missing scope: read")
	}
}

// A reverse proxy can return a non-JSON body; that must still be a usable
// status error rather than a decode failure.
func TestNonJSONErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502 Bad Gateway</html>"))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "whg_test").ListSources(context.Background())

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T), want *APIError", err, err)
	}
	if apiErr.Status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", apiErr.Status)
	}
	if apiErr.Error() == "" {
		t.Error("APIError.Error() is empty for a non-JSON body")
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "localhost:8080", want: "http://localhost:8080"},
		{in: "  http://localhost:8080/  ", want: "http://localhost:8080"},
		{in: "https://gw.example.com", want: "https://gw.example.com"},
		{in: "", wantErr: true},
		{in: "ftp://example.com", wantErr: true},
		{in: "http://", wantErr: true},
	}
	for _, tt := range tests {
		got, err := NormalizeURL(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("NormalizeURL(%q) = %q, want error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeURL(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("NormalizeURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
