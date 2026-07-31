// Package dashboard embeds the built dashboard SPA and serves it from the
// gateway's own port, so a self-hoster runs one binary and gets both the API
// and the UI.
package dashboard

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// The `all:` prefix keeps dist/.gitkeep in the pattern, which is what makes
// this compile on a checkout where the SPA has never been built. `make web`
// (run by `make build`) fills dist/ with the real output.
//
//go:embed all:dist
var dist embed.FS

// Handler serves the SPA: real files straight out of dist/, and index.html for
// anything else so client-side routes survive a page refresh.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // Impossible: dist is embedded directly above.
	}
	return handlerFor(sub)
}

func handlerFor(fsys fs.FS) http.Handler {
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		return http.HandlerFunc(notBuilt)
	}
	return &spa{fsys: fsys, index: index}
}

type spa struct {
	fsys  fs.FS
	index []byte
}

func (s *spa) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// This handler is registered on "/", so it is also where every unmatched
	// API and ingest path lands. Those callers want JSON, not an HTML shell.
	if isAPIPath(r.URL.Path) {
		notFoundJSON(w)
		return
	}

	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	f, err := s.fsys.Open(name)
	if err != nil {
		if strings.HasPrefix(name, "assets/") {
			http.NotFound(w, r)
			return
		}
		s.serveIndex(w, r)
		return
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		s.serveIndex(w, r)
		return
	}
	// Only embed.FS and the test FS reach this, and both give seekable files;
	// anything else falls back to the shell rather than serving a partial file.
	content, ok := f.(io.ReadSeeker)
	if !ok {
		s.serveIndex(w, r)
		return
	}

	// Vite fingerprints everything under assets/, so those URLs are safe to
	// cache forever. Anything else keeps the default revalidate behaviour.
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), content)
}

// index.html must never be cached: it carries the hashed asset URLs, so a
// stale copy points a browser at assets that no longer exist after an upgrade.
func (s *spa) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(s.index))
}

// notBuilt answers when the binary was compiled without a built SPA — the
// result of a plain `go build ./...`. The API is fine and only the UI is
// missing, so say exactly that instead of returning a bare 404.
func notBuilt(w http.ResponseWriter, r *http.Request) {
	if isAPIPath(r.URL.Path) {
		notFoundJSON(w)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>Webhook Gateway</title>`+
		`<p>Dashboard not built into this binary. Run <code>make build</code> and restart.</p>`)
}

func isAPIPath(p string) bool {
	return strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/ingest/")
}

func notFoundJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `{"error":"not found"}`)
}
