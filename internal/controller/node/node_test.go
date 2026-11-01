package node_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/controller"
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
