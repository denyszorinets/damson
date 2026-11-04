package node_test

import (
	"context"
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
