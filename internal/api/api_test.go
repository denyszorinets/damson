package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"go.uber.org/goleak"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite"
	"github.com/denyszorinets/damson/internal/webui"
)

func TestMain(m *testing.M) {
	code := m.Run()
	stopPostgres()
	if code == 0 {
		if err := goleak.Find(); err != nil {
			fmt.Fprintf(os.Stderr, "goleak: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

// fakeStore is a Checker with injectable failures.
type fakeStore struct {
	pingErr    error
	version    int64
	versionErr error
}

func (f fakeStore) Ping(context.Context) error { return f.pingErr }
func (f fakeStore) MigrationVersion(context.Context) (int64, error) {
	return f.version, f.versionErr
}

func ui() http.Handler {
	return webui.Handler(fstest.MapFS{"index.html": {Data: []byte("<html>shell</html>")}})
}

func newServer(t *testing.T, c api.Checker, logs *bytes.Buffer) *api.Server {
	t.Helper()
	return api.NewServer(api.Config{
		Addr: "127.0.0.1:0", Store: c, UI: ui(),
		Logger: slog.New(slog.NewJSONHandler(logs, nil)),
	})
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func sqliteStore(t *testing.T, migrate bool) *sqlite.Store {
	t.Helper()
	ctx := context.Background()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if migrate {
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestHandlers(t *testing.T) {
	closed := sqliteStore(t, true)
	_ = closed.Close()
	tests := []struct {
		name       string
		store      api.Checker
		method     string
		target     string
		wantStatus int
		wantType   string // content type prefix
		wantBody   string
	}{
		{"healthz ok", fakeStore{pingErr: errors.New("db down")}, "GET", "/healthz", 200, "application/json", `"ok"`},
		{"healthz post", fakeStore{}, "POST", "/healthz", 405, api.ProblemContentType, "method-not-allowed"},
		{"readyz sqlite migrated", sqliteStore(t, true), "GET", "/readyz", 200, "application/json", `"ready"`},
		{"readyz sqlite not migrated", sqliteStore(t, false), "GET", "/readyz", 503, api.ProblemContentType, "unavailable"},
		{"readyz sqlite closed", closed, "GET", "/readyz", 503, api.ProblemContentType, "unavailable"},
		{"readyz ping fails", fakeStore{pingErr: errors.New("secret-dsn")}, "GET", "/readyz", 503, api.ProblemContentType, "unavailable"},
		{"readyz version fails", fakeStore{version: 1, versionErr: errors.New("x")}, "GET", "/readyz", 503, api.ProblemContentType, "unavailable"},
		{"readyz zero version", fakeStore{version: 0}, "GET", "/readyz", 503, api.ProblemContentType, "unavailable"},
		{"healthz head", fakeStore{}, "HEAD", "/healthz", 200, "application/json", ""},
		{"readyz head", fakeStore{version: 1}, "HEAD", "/readyz", 200, "application/json", ""},
		{"readyz ok fake", fakeStore{version: 3}, "GET", "/readyz", 200, "application/json", `"ready"`},
		{"unknown api", fakeStore{}, "GET", "/api/v1/nope", 404, api.ProblemContentType, "not-found"},
		{"api root", fakeStore{}, "POST", "/api/", 404, api.ProblemContentType, "not-found"},
		{"spa root", fakeStore{}, "GET", "/", 200, "text/html", "shell"},
		{"spa deep link", fakeStore{}, "GET", "/jobs/1", 200, "text/html", "shell"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			rec := do(newServer(t, tt.store, &logs).Handler(), tt.method, tt.target)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tt.wantType) {
				t.Errorf("content-type = %q, want prefix %q", ct, tt.wantType)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body %q missing %q", rec.Body, tt.wantBody)
			}
			if strings.Contains(rec.Body.String(), "secret-dsn") {
				t.Errorf("internal error leaked to client: %s", rec.Body)
			}
			if rec.Header().Get("X-Request-Id") == "" {
				t.Error("missing X-Request-Id")
			}
		})
	}
}

func TestProblemBody(t *testing.T) {
	var logs bytes.Buffer
	rec := do(newServer(t, fakeStore{pingErr: errors.New("boom")}, &logs).Handler(), "GET", "/readyz")
	var p api.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != 503 || p.Title == "" || p.Type == "" || p.RequestID != rec.Header().Get("X-Request-Id") {
		t.Errorf("problem = %+v", p)
	}
	// The real cause is logged server-side, with the request id.
	if !strings.Contains(logs.String(), "boom") || !strings.Contains(logs.String(), p.RequestID) {
		t.Errorf("cause not logged: %s", logs.String())
	}
}

func TestWriteError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
		typ  string
	}{
		{"not found", store.ErrNotFound, 404, "not-found"},
		{"wrapped not found", fmt.Errorf("getting job: %w", store.ErrNotFound), 404, "not-found"},
		{"conflict", store.ErrConflict, 409, "conflict"},
		{"stale", fmt.Errorf("cas: %w", store.ErrStale), 409, "stale"},
		{"joined", errors.Join(errors.New("a"), store.ErrNotFound), 404, "not-found"},
		{"unknown", errors.New("pq: password=hunter2"), 500, "internal"},
		{"context deadline is generic 500", context.DeadlineExceeded, 500, "internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			api.WriteError(rec, httptest.NewRequest("GET", "/x", nil), tt.err)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
			if rec.Header().Get("Content-Type") != api.ProblemContentType {
				t.Errorf("content-type = %q", rec.Header().Get("Content-Type"))
			}
			var p api.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if p.Status != tt.want || !strings.HasSuffix(p.Type, tt.typ) {
				t.Errorf("problem = %+v", p)
			}
			if strings.Contains(rec.Body.String(), "hunter2") {
				t.Errorf("leaked: %s", rec.Body)
			}
		})
	}
}

func TestRequestLogOmitsQuery(t *testing.T) {
	var logs bytes.Buffer
	do(newServer(t, fakeStore{}, &logs).Handler(), "GET", "/healthz?token=abc123")
	out := logs.String()
	if !strings.Contains(out, `"path":"/healthz"`) || !strings.Contains(out, `"status":200`) {
		t.Errorf("log line = %s", out)
	}
	if strings.Contains(out, "abc123") {
		t.Errorf("query leaked into log: %s", out)
	}
}

func TestStartStop(t *testing.T) {
	var logs bytes.Buffer
	srv := newServer(t, fakeStore{version: 1}, &logs)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{}}
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://" + srv.Addr() + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := srv.Stop(sctx); err != nil {
		t.Fatal(err)
	}
	client.CloseIdleConnections()
	if _, err := client.Get("http://" + srv.Addr() + "/healthz"); err == nil {
		t.Error("server still accepting after Stop")
	}
	if got := newServer(t, fakeStore{}, &logs).Addr(); got != "" {
		t.Errorf("Addr before Start = %q", got)
	}
	if err := newServer(t, fakeStore{}, &logs).Stop(ctx); err != nil {
		t.Errorf("Stop without Start: %v", err)
	}
}

func TestStartBindError(t *testing.T) {
	var logs bytes.Buffer
	a := newServer(t, fakeStore{}, &logs)
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Stop(context.Background()) }()
	b := api.NewServer(api.Config{Addr: a.Addr(), Store: fakeStore{}})
	if err := b.Start(context.Background()); err == nil {
		t.Error("expected bind error")
	}
}
