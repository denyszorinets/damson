package node_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/controller"
	"github.com/denyszorinets/damson/internal/controller/internalauth"
	"github.com/denyszorinets/damson/internal/controller/node"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite"
)

func start(t *testing.T, migrate bool) *node.Node {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, "file:"+filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if migrate {
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := pluginreg.New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := node.Start(ctx, node.Config{
		Store: st, ControllerID: "ctl-test", Plugins: reg, InsecureDev: true,
		Timing: node.Timing{Dispatch: 20 * time.Millisecond, Reap: 50 * time.Millisecond, Renew: 50 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := n.Stop(sctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	return n
}

func TestNodeMigrated(t *testing.T) {
	n := start(t, true)
	if n.Advancer() == nil {
		t.Fatal("run.advance is off on a migrated database")
	}
	rec := httptest.NewRecorder()
	n.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, controller.ConnectPath, nil))
	if rec.Code == http.StatusServiceUnavailable || rec.Code == http.StatusNotFound {
		t.Fatalf("connect path answered %d; the gateway is not mounted", rec.Code)
	}
}

func TestNodeUnmigratedKeepsServing(t *testing.T) {
	n := start(t, false)
	if n.Advancer() != nil {
		t.Fatal("run.advance started on an unmigrated database")
	}
	rec := httptest.NewRecorder()
	n.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz = %d on an unmigrated database", rec.Code)
	}
}

// With a cluster secret the node serves the internal endpoint on its own port,
// signed requests only, and the public handler does not serve it at all.
func TestNodeInternalListener(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(ctx, "file:"+filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	reg, err := pluginreg.New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("c", 40)
	n, err := node.Start(ctx, node.Config{
		Store: st, ControllerID: "ctl-test", Plugins: reg, InsecureDev: true,
		ClusterSecret: secret, InternalListener: ln, InternalURL: "http://" + ln.Addr().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := n.Stop(sctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	}
	t.Cleanup(stop)

	url := "http://" + ln.Addr().String() + "/internal/v1/executions/e1/tail?attempt_id=a1"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned internal request = %d; want 401", resp.StatusCode)
	}
	keys, _ := internalauth.NewKeyring(secret, "")
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	keys.Sign(req, "ctl-other", "ctl-test", time.Now())
	if resp, err = http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound { // authenticated, but nothing buffered for e1
		t.Fatalf("signed internal request = %d; want 404", resp.StatusCode)
	}
	rec := httptest.NewRecorder()
	n.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/internal/v1/executions/e1/tail?attempt_id=a1", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("the public handler answered the internal route with %d", rec.Code)
	}
	stop()
	if _, err := http.Get(url); err == nil {
		t.Fatal("the internal listener still answers after Stop")
	}
}

func startSingle(t *testing.T, st store.Store, id string) (*node.Node, error) {
	t.Helper()
	reg, err := pluginreg.New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return node.Start(context.Background(), node.Config{
		Store: st, ControllerID: id, Plugins: reg, InsecureDev: true, SingleController: true,
		Timing: node.Timing{LeaderTTL: 600 * time.Millisecond, LeaderRenew: 200 * time.Millisecond},
	})
}

func stopNode(t *testing.T, n *node.Node) {
	t.Helper()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := n.Stop(sctx); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

func migratedStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

// A second controller on the same SQLite database refuses to start while the
// first one is alive, and may start as soon as the first one stopped.
func TestSingleControllerGuard(t *testing.T) {
	st := migratedStore(t)
	first, err := startSingle(t, st, "ctl-1")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := startSingle(t, st, "ctl-2"); err == nil {
		stopNode(t, n)
		t.Fatal("a second controller started on a SQLite database in use")
	} else if !strings.Contains(err.Error(), "SQLite supports one controller") {
		t.Fatalf("error = %v", err)
	}
	stopNode(t, first) // releases the lease
	start := time.Now()
	second, err := startSingle(t, st, "ctl-2")
	if err != nil {
		t.Fatalf("start after a graceful stop: %v", err)
	}
	defer stopNode(t, second)
	if took := time.Since(start); took > 400*time.Millisecond {
		t.Fatalf("start after a graceful stop waited %s", took)
	}
}

// A crashed controller leaves its lease behind; the next start waits it out.
func TestSingleControllerGuardWaitsOutACrash(t *testing.T) {
	st := migratedStore(t)
	now := time.Now()
	if _, ok, err := st.AcquireLeader(context.Background(), "sqlite-controller", "crashed", now, now.Add(500*time.Millisecond)); err != nil || !ok {
		t.Fatalf("seeding the stale lease: %v %v", ok, err)
	}
	n, err := startSingle(t, st, "ctl-2")
	if err != nil {
		t.Fatalf("start with a stale lease: %v", err)
	}
	defer stopNode(t, n)
	if time.Since(now) < 450*time.Millisecond {
		t.Fatal("started before the stale lease expired")
	}
}

// Without the flag (PostgreSQL) several controllers share a database.
func TestNoGuardWithoutTheFlag(t *testing.T) {
	st := migratedStore(t)
	reg, _ := pluginreg.New(nil, nil)
	var nodes []*node.Node
	for _, id := range []string{"a", "b"} {
		n, err := node.Start(context.Background(), node.Config{Store: st, ControllerID: id, Plugins: reg, InsecureDev: true})
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, n)
	}
	for _, n := range nodes {
		stopNode(t, n)
	}
}

// A second process on an SQLite database that is already in use is refused
// before it seeds anything: the running controller's inventory stays as it is.
func TestSecondProcessDoesNotSeed(t *testing.T) {
	st := migratedStore(t)
	first, err := startSingle(t, st, "ctl-1")
	if err != nil {
		t.Fatal(err)
	}
	defer stopNode(t, first)

	reg, err := pluginreg.New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := node.Start(context.Background(), node.Config{
		Store: st, ControllerID: "ctl-2", Plugins: reg, InsecureDev: true, SingleController: true, DevFile: devFile(t, false),
		Timing: node.Timing{LeaderTTL: 600 * time.Millisecond, LeaderRenew: 200 * time.Millisecond},
	})
	if err == nil {
		stopNode(t, second)
		t.Fatal("a second process started on a database in use")
	}
	if _, err := st.GetNode(context.Background(), "dev-1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the refused process seeded the inventory: %v", err)
	}
}
