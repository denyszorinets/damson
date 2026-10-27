package webui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/denyszorinets/damson/internal/webui"
	"github.com/denyszorinets/damson/web"
)

func built() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                  {Data: []byte("<html>shell</html>")},
		"robots.txt":                  {Data: []byte("User-agent: *")},
		"_app/immutable/entry/app.js": {Data: []byte("console.log(1)")},
	}
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestHandler(t *testing.T) {
	h := webui.Handler(built())
	tests := []struct {
		name, method, target string
		status               int
		body, cache          string
	}{
		{"root", "GET", "/", 200, "shell", "no-cache"},
		{"static file", "GET", "/robots.txt", 200, "User-agent", ""},
		{"immutable asset", "GET", "/_app/immutable/entry/app.js", 200, "console.log", "public, max-age=31536000, immutable"},
		{"spa route", "GET", "/jobs/42/runs", 200, "shell", "no-cache"},
		{"traversal is cleaned", "GET", "/../../etc/passwd", 200, "shell", "no-cache"},
		{"missing asset", "GET", "/_app/immutable/missing.js", 404, "", ""},
		{"missing file with extension", "GET", "/nope.png", 404, "", ""},
		{"head", "HEAD", "/jobs", 200, "", "no-cache"},
		{"post rejected", "POST", "/", 405, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(h, tc.method, tc.target)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if !strings.Contains(rec.Body.String(), tc.body) {
				t.Errorf("body = %q, want it to contain %q", rec.Body.String(), tc.body)
			}
			if got := rec.Header().Get("Cache-Control"); got != tc.cache {
				t.Errorf("Cache-Control = %q, want %q", got, tc.cache)
			}
		})
	}
}

func TestHandlerNotBuilt(t *testing.T) {
	rec := do(webui.Handler(fstest.MapFS{".gitkeep": {}}), "GET", "/")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "make web") {
		t.Errorf("body = %q, want a hint to run make web", rec.Body.String())
	}
}

// TestEmbedded checks that the embedded tree is usable whether or not
// "make web" ran: either the real shell or the not-built notice.
func TestEmbedded(t *testing.T) {
	rec := do(webui.Handler(web.Build()), "GET", "/some/route")
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 200 or 503", rec.Code)
	}
}
