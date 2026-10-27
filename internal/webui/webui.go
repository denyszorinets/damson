// Package webui serves the embedded SvelteKit single-page app.
package webui

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

const indexFile = "index.html"

// Handler serves the SPA found in root (the contents of web/build).
//
// Existing files are served as-is, with long-lived caching for the
// content-hashed files under _app/immutable/. A missing path under _app/ or a
// path that looks like a file (has an extension) is a 404. Any other unknown
// path is a client-side route and gets index.html, so deep links work after a
// reload. Only GET and HEAD are allowed. If root has no index.html (the UI was
// not built) every request gets a 503 with a short explanation.
//
// Mount it after the API and health routes so it only sees what they did not
// claim.
func Handler(root fs.FS) http.Handler {
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if _, err := fs.Stat(root, indexFile); err != nil {
			http.Error(w, "web UI not built: run \"make web\" and rebuild the binary", http.StatusServiceUnavailable)
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			serveIndex(w, r, root)
			return
		}
		if info, err := fs.Stat(root, name); err == nil && !info.IsDir() {
			if strings.HasPrefix(name, "_app/immutable/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(name, "_app/") || path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r, root)
	})
}

// serveIndex writes index.html with a 200 status, regardless of the request
// path, and tells caches to revalidate it so a new build is picked up.
func serveIndex(w http.ResponseWriter, r *http.Request, root fs.FS) {
	data, err := fs.ReadFile(root, indexFile)
	if err != nil {
		http.Error(w, "web UI not readable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, indexFile, time.Time{}, bytes.NewReader(data))
}
