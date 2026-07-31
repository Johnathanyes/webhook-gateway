package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// A stand-in for a real Vite build: a shell plus one fingerprinted asset.
func builtFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                &fstest.MapFile{Data: []byte(`<div id="root"></div>`)},
		"assets/index-abc123.js":    &fstest.MapFile{Data: []byte(`console.log("hi")`)},
		"favicon.svg":               &fstest.MapFile{Data: []byte(`<svg/>`)},
	}
}

func get(t *testing.T, h http.Handler, target string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Result()
}

func TestServesShellAtRoot(t *testing.T) {
	res := get(t, handlerFor(builtFS()), "/")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("cache-control = %q, want no-cache", cc)
	}
}

// A client-side route has no file behind it; a refresh on one must still load
// the shell rather than 404.
func TestUnknownPathFallsBackToShell(t *testing.T) {
	res := get(t, handlerFor(builtFS()), "/events/123")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content-type = %q, want text/html", ct)
	}
}

func TestServesFingerprintedAssetImmutably(t *testing.T) {
	res := get(t, handlerFor(builtFS()), "/assets/index-abc123.js")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("cache-control = %q, want immutable", cc)
	}
}

// The handler sits on "/", so unmatched API routes land here. An API client
// must get JSON 404s, never the HTML shell.
func TestUnmatchedAPIPathsGetJSON404(t *testing.T) {
	for _, target := range []string{"/api/nope", "/ingest/nope"} {
		res := get(t, handlerFor(builtFS()), target)

		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", target, res.StatusCode)
		}
		if ct := res.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s: content-type = %q, want application/json", target, ct)
		}
	}
}

// `go build ./...` with no `npm run build` produces a binary whose dist/ holds
// only .gitkeep. That must still serve something explanatory, and must not
// break the API's 404s.
func TestUnbuiltDistExplainsItself(t *testing.T) {
	h := handlerFor(fstest.MapFS{".gitkeep": &fstest.MapFile{}})

	res := get(t, h, "/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content-type = %q, want text/html", ct)
	}

	if res := get(t, h, "/api/nope"); res.StatusCode != http.StatusNotFound {
		t.Errorf("api status = %d, want 404", res.StatusCode)
	}
}

// The embedded FS is whatever the build produced, so this only asserts the
// wiring works — Handler() must not panic and must answer.
func TestHandlerServesEmbeddedFS(t *testing.T) {
	if res := get(t, Handler(), "/"); res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
}
