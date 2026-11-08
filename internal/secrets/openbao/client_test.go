package openbao

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/secrets"
	"github.com/denyszorinets/damson/internal/secrets/openbao/baotest"
	"github.com/denyszorinets/damson/internal/store"
)

var tNow = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

type auditSink struct {
	mu  sync.Mutex
	evs []store.AuditEvent
}

func (a *auditSink) AppendAuditEvent(_ context.Context, ev store.AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evs = append(a.evs, ev)
	return nil
}
func (a *auditSink) ListAuditEvents(context.Context, store.AuditFilter, int) ([]store.AuditEvent, error) {
	return nil, nil
}

func actor() context.Context {
	return store.WithAudit(context.Background(), store.AuditMeta{Actor: "test:actor"})
}

func cfgFor(srv *baotest.Server, a Auth) Config {
	return Config{Address: srv.URL(), AllowPlainHTTP: true, Prefix: "damson", Auth: a}
}

func rootAuth() Auth { return Auth{Method: AuthToken, Token: baotest.RootToken} }

func start(t *testing.T, cfg Config) (*Client, *Backend) {
	t.Helper()
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.sleep = func(ctx context.Context, d time.Duration) bool { // request retries are instant; the renewer sleeps until stopped
		if d >= time.Second {
			<-ctx.Done()
			return false
		}
		return ctx.Err() == nil
	}
	b := NewBackend(c, &auditSink{}, func() time.Time { return tNow })
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	return c, b
}

func TestNewValidation(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*Config)
		want string
	}{
		{"plain http refused", func(c *Config) { c.Address = "http://bao:8200"; c.AllowPlainHTTP = false }, "plain http"},
		{"path in address", func(c *Config) { c.Address = "https://bao:8200/v1" }, "scheme and host only"},
		{"userinfo", func(c *Config) { c.Address = "https://u:p@bao:8200" }, "scheme and host only"},
		{"not a url", func(c *Config) { c.Address = "bao" }, "http(s) URL"},
		{"bad mount", func(c *Config) { c.Mount = "a/../b" }, "mount"},
		{"bad prefix", func(c *Config) { c.Prefix = "/x" }, "prefix"},
		{"empty prefix", func(c *Config) { c.Prefix = "" }, "prefix"},
		{"bad auth mount", func(c *Config) { c.Auth.Mount = "../x" }, "auth mount"},
		{"cache ttl too long", func(c *Config) { c.CacheTTL = time.Minute }, "cache_ttl"},
		{"bad ca", func(c *Config) { c.Address = "https://bao:8200"; c.CAPEM = []byte("junk") }, "tls_ca_file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Address: "https://bao:8200", Prefix: "damson", Auth: rootAuth()}
			tc.mod(&cfg)
			if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestNamespaceAndTokenHeaders(t *testing.T) {
	srv := baotest.Start(t)
	srv.Namespace = "team-a"
	cfg := cfgFor(srv, rootAuth())
	cfg.Namespace = "team-a"
	_, b := start(t, cfg)
	if _, _, err := b.Put(actor(), "p", "x", "token", map[string]string{"token": "v"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, r := range srv.Requests() {
		if r.Namespace != "team-a" || r.Token != baotest.RootToken {
			t.Fatalf("request %+v lacks the namespace or token", r)
		}
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "" {
			leaked.Store(true)
		}
	}))
	defer other.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "lookup-self") {
			_, _ = w.Write([]byte(`{"data":{"ttl":0,"renewable":false}}`))
			return
		}
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer front.Close()
	c, err := New(Config{Address: front.URL, AllowPlainHTTP: true, Prefix: "damson", Auth: Auth{Method: AuthToken, Token: "s.secret-token"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(context.Background()) }()
	b := NewBackend(c, &auditSink{}, time.Now)
	_, err = b.Get(context.Background(), "p", "x")
	if err == nil || leaked.Load() {
		t.Fatalf("err = %v, token leaked = %v", err, leaked.Load())
	}
	if strings.Contains(err.Error(), "s.secret-token") {
		t.Fatalf("the token is in the error: %v", err)
	}
}

func TestReadsRetryButWritesDoNot(t *testing.T) {
	srv := baotest.Start(t)
	var gets, posts atomic.Int32
	srv.Fail = func(r baotest.Request) int {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.Path, "/data/"):
			if gets.Add(1) <= 2 {
				return http.StatusServiceUnavailable
			}
		case r.Method == http.MethodPost && strings.Contains(r.Path, "/data/"):
			posts.Add(1)
			return http.StatusServiceUnavailable
		}
		return 0
	}
	_, b := start(t, cfgFor(srv, rootAuth()))
	srv.SetValue("secret/damson/p/x", map[string]any{"token": "v"})
	if _, err := b.Get(actor(), "p", "x"); err != nil {
		t.Fatalf("a read retried on 503 failed: %v", err)
	}
	if gets.Load() != 3 {
		t.Fatalf("gets = %d, want 3", gets.Load())
	}
	if _, _, err := b.Put(actor(), "p", "y", "token", map[string]string{"token": "v"}, nil); err == nil {
		t.Fatal("put on 503 succeeded")
	}
	if posts.Load() != 1 {
		t.Fatalf("a write was sent %d times, want 1", posts.Load())
	}
}

func TestBadStoredValuesAreUnavailable(t *testing.T) {
	srv := baotest.Start(t)
	_, b := start(t, cfgFor(srv, rootAuth()))
	srv.SetValue("secret/damson/p/num", map[string]any{"token": 42})
	srv.SetValue("secret/damson/p/field", map[string]any{"Bad-Field": "v"})
	for _, name := range []string{"num", "field"} {
		_, err := b.Get(actor(), "p", name)
		if !errors.Is(err, secrets.ErrUnavailable) {
			t.Fatalf("%s: err = %v; want unavailable", name, err)
		}
		if strings.Contains(err.Error(), "42") {
			t.Fatalf("a value is in the error: %v", err)
		}
	}
}

func TestCrashWindowListsAsGeneric(t *testing.T) {
	srv := baotest.Start(t)
	_, b := start(t, cfgFor(srv, rootAuth()))
	srv.SetValue("secret/damson/p/orphan", map[string]any{"token": "v"}) // data, no damson metadata
	m, err := b.Meta(actor(), "p", "orphan")
	if err != nil || m.Type != "generic" || m.CurrentVersion != 1 {
		t.Fatalf("meta = %+v %v", m, err)
	}
	// The next put takes the caller's type and writes the metadata.
	if m, _, err := b.Put(actor(), "p", "orphan", "generic", map[string]string{"token": "w"}, nil); err != nil || m.CurrentVersion != 2 {
		t.Fatalf("put = %+v %v", m, err)
	}
}

func TestReadOnly(t *testing.T) {
	srv := baotest.Start(t)
	cfg := cfgFor(srv, rootAuth())
	cfg.ReadOnly = true
	_, b := start(t, cfg)
	srv.SetValue("secret/damson/p/x", map[string]any{"token": "v"})
	if _, _, err := b.Put(actor(), "p", "x", "token", map[string]string{"token": "w"}, nil); !errors.Is(err, secrets.ErrReadOnly) {
		t.Fatalf("put = %v", err)
	}
	if err := b.Delete(actor(), "p", "x"); !errors.Is(err, secrets.ErrReadOnly) {
		t.Fatalf("delete = %v", err)
	}
	if got, err := b.Get(actor(), "p", "x"); err != nil || got["token"] != "v" {
		t.Fatalf("get = %v %v", got, err)
	}
	for _, r := range srv.Requests() {
		if r.Method == http.MethodPost && strings.Contains(r.Path, "/data/") || r.Method == http.MethodDelete {
			t.Fatalf("a read-only backend sent %+v", r)
		}
	}
}

func TestForeignMetadataIsKept(t *testing.T) {
	srv := baotest.Start(t)
	_, b := start(t, cfgFor(srv, rootAuth()))
	srv.SetValue("secret/damson/p/x", map[string]any{"token": "v"})
	// Someone else's metadata on the same secret.
	hc := &http.Client{}
	req, _ := http.NewRequest(http.MethodPost, srv.URL()+"/v1/secret/metadata/damson/p/x", strings.NewReader(`{"custom_metadata":{"owner":"team-a"}}`))
	req.Header.Set("X-Vault-Token", baotest.RootToken)
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if _, _, err := b.Put(actor(), "p", "x", "token", map[string]string{"token": "w"}, nil); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Data struct {
			Custom map[string]string `json:"custom_metadata"`
		} `json:"data"`
	}
	if err := b.c.do(context.Background(), http.MethodGet, "secret/metadata/damson/p/x", nil, &out, true); err != nil {
		t.Fatal(err)
	}
	if out.Data.Custom["owner"] != "team-a" || out.Data.Custom[metaType] != "token" {
		t.Fatalf("custom metadata = %v", out.Data.Custom)
	}
}

func TestLoginMethods(t *testing.T) {
	dir := t.TempDir()
	write := func(name, v string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	srv := baotest.Start(t)
	t.Run("approle", func(t *testing.T) {
		_, b := start(t, cfgFor(srv, Auth{Method: AuthAppRole, RoleID: "role-1", SecretIDFile: write("sid", "secret-1")}))
		if _, _, err := b.Put(actor(), "p", "x", "token", map[string]string{"token": "v"}, nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("kubernetes", func(t *testing.T) {
		_, b := start(t, cfgFor(srv, Auth{Method: AuthKubernetes, Role: "damson", SATokenFP: write("jwt", "jwt-1")}))
		if _, err := b.Get(actor(), "p", "x"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("rejected login fails the start", func(t *testing.T) {
		c, _ := New(cfgFor(srv, Auth{Method: AuthAppRole, RoleID: "role-1", SecretIDFile: write("bad", "wrong")}))
		err := c.Start(context.Background())
		if err == nil || strings.Contains(err.Error(), "wrong") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("missing credential file fails the start", func(t *testing.T) {
		c, _ := New(cfgFor(srv, Auth{Method: AuthToken, TokenFile: filepath.Join(dir, "none")}))
		if err := c.Start(context.Background()); err == nil {
			t.Fatal("start succeeded")
		}
	})
	t.Run("login is revoked on stop", func(t *testing.T) {
		before := srv.RevokeCalls()
		c, _ := start(t, cfgFor(srv, Auth{Method: AuthAppRole, RoleID: "role-1", SecretIDFile: write("sid2", "secret-1")}))
		if err := c.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if srv.RevokeCalls() != before+1 {
			t.Fatalf("revoke calls = %d, want %d", srv.RevokeCalls(), before+1)
		}
	})
}

func TestOutageStartsDegradedAndRecovers(t *testing.T) {
	srv := baotest.Start(t)
	var down atomic.Bool
	down.Store(true)
	srv.Fail = func(baotest.Request) int {
		if down.Load() {
			return http.StatusServiceUnavailable
		}
		return 0
	}
	cfg := cfgFor(srv, Auth{Method: AuthAppRole, RoleID: "role-1", SecretID: "secret-1"})
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var waits atomic.Int32
	c.sleep = func(ctx context.Context, d time.Duration) bool {
		if waits.Add(1) == 2 {
			down.Store(false)
		}
		time.Sleep(time.Millisecond)
		return ctx.Err() == nil
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("an outage must not fail the start: %v", err)
	}
	defer func() { _ = c.Stop(context.Background()) }()
	deadline := time.Now().Add(3 * time.Second)
	for srv.Logins() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if srv.Logins() == 0 {
		t.Fatal("the client never logged in after the outage")
	}
}

func TestForbiddenTriggersOneRelogin(t *testing.T) {
	srv := baotest.Start(t)
	c, b := start(t, cfgFor(srv, Auth{Method: AuthAppRole, RoleID: "role-1", SecretID: "secret-1"}))
	srv.SetValue("secret/damson/p/x", map[string]any{"token": "v"})
	srv.Revoke(c.currentToken()) // the token expired behind the client's back
	before := srv.Logins()
	got, err := b.Get(actor(), "p", "x")
	if err != nil || got["token"] != "v" {
		t.Fatalf("get after expiry = %v %v", got, err)
	}
	if srv.Logins() != before+1 {
		t.Fatalf("logins = %d, want %d", srv.Logins(), before+1)
	}
}

func TestPersistent403IsUnavailable(t *testing.T) {
	srv := baotest.Start(t)
	srv.Fail = func(r baotest.Request) int {
		if strings.Contains(r.Path, "/data/") {
			return http.StatusForbidden
		}
		return 0
	}
	_, b := start(t, cfgFor(srv, Auth{Method: AuthAppRole, RoleID: "role-1", SecretID: "secret-1"}))
	_, err := b.Get(actor(), "p", "x")
	if !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("err = %v; want unavailable", err)
	}
	if n := len(srv.Requests()); n > 12 {
		t.Fatalf("%d requests for one failing read: the retry is unbounded", n)
	}
}

func TestRenewsAtTwoThirdsOfTheLease(t *testing.T) {
	srv := baotest.Start(t)
	srv.Lease = 90
	cfg := cfgFor(srv, Auth{Method: AuthAppRole, RoleID: "role-1", SecretID: "secret-1"})
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	waits := make(chan time.Duration, 8)
	c.sleep = func(ctx context.Context, d time.Duration) bool {
		select {
		case waits <- d:
		default:
		}
		if d >= time.Second {
			time.Sleep(2 * time.Millisecond)
			return ctx.Err() == nil
		}
		return ctx.Err() == nil
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(context.Background()) }()
	select {
	case d := <-waits:
		if d != 60*time.Second {
			t.Fatalf("first wait = %v, want 60s (two thirds of 90s)", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the renewer never waited")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		srv.Logins() // lock-step with the server
		if srv.RenewCalls() > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the token was never renewed")
}

func TestTLSWithCustomCA(t *testing.T) {
	srv, ca := baotest.StartTLS(t)
	cfg := Config{Address: srv.URL(), Prefix: "damson", Auth: rootAuth(), CAPEM: ca}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start with the right CA: %v", err)
	}
	_ = c.Stop(context.Background())
	// Without the CA the certificate is refused: verification cannot be turned off.
	c2, _ := New(Config{Address: srv.URL(), Prefix: "damson", Auth: rootAuth()})
	if err := c2.Start(context.Background()); err == nil {
		_ = c2.Stop(context.Background())
		t.Fatal("an unknown certificate was accepted")
	}
}

// A soft-deleted or metadata-only entry is not a live secret, and dropping it
// must not shorten a page: that would end the caller's paging early.
func TestListSkipsDeadEntriesWithoutShorteningPages(t *testing.T) {
	srv := baotest.Start(t)
	_, b := start(t, cfgFor(srv, rootAuth()))
	ctx := actor()
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		if _, _, err := b.Put(ctx, "p", n, "token", map[string]string{"token": "v"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	srv.SoftDelete("secret/damson/p/a")
	srv.SoftDelete("secret/damson/p/c")
	srv.SetMetadataOnly("secret/damson/p/b0")
	page, err := b.List(ctx, "p", "", "", 2)
	if err != nil || len(page) != 2 || page[0].Name != "b" || page[1].Name != "d" {
		t.Fatalf("first page = %+v %v; want b, d", page, err)
	}
	next, err := b.List(ctx, "p", "p", "d", 2)
	if err != nil || len(next) != 1 || next[0].Name != "e" {
		t.Fatalf("second page = %+v %v; want e", next, err)
	}
	if _, err := b.Meta(ctx, "p", "b0"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("a metadata-only entry is a secret: %v", err)
	}
	if _, created, err := b.Put(ctx, "p", "b0", "token", map[string]string{"token": "v"}, i64(0)); err != nil || !created {
		t.Fatalf("create over metadata only: created=%v err=%v", created, err)
	}
}

func i64(n int64) *int64 { return &n }

// If the metadata write is refused after the data landed, the new version is
// still audited and the cache does not keep serving the old value.
func TestPartialWriteIsAuditedAndEvicted(t *testing.T) {
	srv := baotest.Start(t)
	cfg := cfgFor(srv, rootAuth())
	cfg.CacheTTL = 30 * time.Second
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sink := &auditSink{}
	b := NewBackend(c, sink, func() time.Time { return tNow })
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Stop(context.Background()) }()
	ctx := actor()
	if _, _, err := b.Put(ctx, "p", "x", "token", map[string]string{"token": "one"}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.GetForDispatch(ctx, "p", "x"); got["token"] != "one" {
		t.Fatalf("first = %v", got)
	}
	srv.Fail = func(r baotest.Request) int {
		if r.Method == http.MethodPost && strings.Contains(r.Path, "/metadata/") {
			return http.StatusForbidden
		}
		return 0
	}
	before := len(sink.evs)
	if _, _, err := b.Put(ctx, "p", "x", "token", map[string]string{"token": "two"}, nil); err == nil {
		t.Fatal("put with a refused metadata write succeeded")
	}
	if len(sink.evs) != before+1 || !strings.Contains(string(sink.evs[before].Details), `"version":2`) {
		t.Fatalf("the written version was not audited: %d events", len(sink.evs))
	}
	srv.Fail = nil
	if got, _ := b.GetForDispatch(ctx, "p", "x"); got["token"] != "two" {
		t.Fatalf("the cache served %v after a write", got)
	}
}

// A policy denial is not a dead token: no login per denied read.
func TestDeniedReadsDoNotLogIn(t *testing.T) {
	srv := baotest.Start(t)
	srv.Fail = func(r baotest.Request) int {
		if strings.Contains(r.Path, "/data/") && r.Method == http.MethodGet {
			return http.StatusForbidden
		}
		return 0
	}
	_, b := start(t, cfgFor(srv, Auth{Method: AuthAppRole, RoleID: "role-1", SecretID: "secret-1"}))
	before := srv.Logins()
	for range 5 {
		if _, err := b.Get(actor(), "p", "x"); !errors.Is(err, secrets.ErrUnavailable) {
			t.Fatalf("err = %v", err)
		}
	}
	if srv.Logins() != before {
		t.Fatalf("%d logins for denied reads", srv.Logins()-before)
	}
}

// An expired token costs one login, however many callers hit it at once.
func TestExpiredTokenIsReplacedOnce(t *testing.T) {
	srv := baotest.Start(t)
	c, b := start(t, cfgFor(srv, Auth{Method: AuthAppRole, RoleID: "role-1", SecretID: "secret-1"}))
	srv.SetValue("secret/damson/p/x", map[string]any{"token": "v"})
	srv.Revoke(c.currentToken())
	before := srv.Logins()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := b.Get(actor(), "p", "x"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if srv.Logins() != before+1 {
		t.Fatalf("logins = %d, want %d", srv.Logins(), before+1)
	}
}

// An outage during re-login must not turn a usable secret into a permanent
// failure.
func TestOutageDuringReloginIsTransient(t *testing.T) {
	srv := baotest.Start(t)
	c, b := start(t, cfgFor(srv, Auth{Method: AuthAppRole, RoleID: "role-1", SecretID: "secret-1"}))
	srv.SetValue("secret/damson/p/x", map[string]any{"token": "v"})
	srv.Revoke(c.currentToken())
	srv.Fail = func(r baotest.Request) int {
		if strings.Contains(r.Path, "/login") {
			return http.StatusServiceUnavailable
		}
		return 0
	}
	_, err := b.Get(actor(), "p", "x")
	if err == nil || errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("err = %v; want a transient error", err)
	}
}

// An oversized or malformed answer is a permanent failure, not an endless retry.
func TestMalformedAnswerIsUnavailable(t *testing.T) {
	srv := baotest.Start(t)
	_, b := start(t, cfgFor(srv, rootAuth()))
	srv.SetValue("secret/damson/p/big", map[string]any{"token": strings.Repeat("x", 3<<20)})
	if _, err := b.Get(actor(), "p", "big"); !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("err = %v; want unavailable", err)
	}
}

// Neither the AppRole secret id, the issued client token nor a Kubernetes JWT
// reaches a log line or an error text through login, renewal and re-login.
func TestLoginCredentialsNeverLogged(t *testing.T) {
	srv := baotest.Start(t)
	var buf lockedBuf
	cfg := cfgFor(srv, Auth{Method: AuthAppRole, RoleID: "role-1", SecretID: "secret-1"})
	cfg.Logger = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c, b := start(t, cfg)
	srv.SetValue("secret/damson/p/x", map[string]any{"token": "v"})
	issued := c.currentToken()
	srv.Revoke(issued)
	srv.Fail = func(r baotest.Request) int {
		if strings.Contains(r.Path, "renew-self") {
			return http.StatusInternalServerError
		}
		return 0
	}
	_, _ = b.Get(actor(), "p", "x")
	c.refresh(context.Background(), true)
	_, err := b.Get(actor(), "p", "missing")
	text := buf.String() + fmt.Sprint(err)
	for _, secret := range []string{"secret-1", issued, c.currentToken()} {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("%q leaked: %s", secret, text)
		}
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// A rotated token file is picked up when the client refreshes.
func TestTokenFileRotationIsPickedUp(t *testing.T) {
	srv := baotest.Start(t)
	dir := t.TempDir()
	f := filepath.Join(dir, "token")
	if err := os.WriteFile(f, []byte(baotest.RootToken), 0o600); err != nil {
		t.Fatal(err)
	}
	c, b := start(t, cfgFor(srv, Auth{Method: AuthToken, TokenFile: f}))
	srv.SetValue("secret/damson/p/x", map[string]any{"token": "v"})
	srv.AddToken("s.rotated")
	if err := os.WriteFile(f, []byte("s.rotated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.refresh(context.Background(), false)
	if c.currentToken() != "s.rotated" {
		t.Fatalf("token = %q", c.currentToken())
	}
	if _, err := b.Get(actor(), "p", "x"); err != nil {
		t.Fatal(err)
	}
}
