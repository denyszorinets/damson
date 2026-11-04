//go:build chaos

package chaos

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/agent"
	"github.com/denyszorinets/damson/internal/controller/advance"
	"github.com/denyszorinets/damson/internal/controller/node"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres"
	"github.com/denyszorinets/damson/internal/store/tokens"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Short intervals so a scenario takes seconds. LeaseTTL stays well above the
// worst reconnect time, so "the step was not lost" is a real assertion.
var timing = node.Timing{
	Heartbeat: 50 * time.Millisecond, Flush: 50 * time.Millisecond, PluginSync: 100 * time.Millisecond, ReconnectJitter: 200 * time.Millisecond,
	Dispatch: 20 * time.Millisecond, Renew: 50 * time.Millisecond, Reap: 50 * time.Millisecond, Cancel: 50 * time.Millisecond, IncapableRetry: 100 * time.Millisecond,
	LeaseTTL: 1500 * time.Millisecond, LeaderTTL: 2 * time.Second, LeaderRenew: 500 * time.Millisecond, AdvancePoll: 20 * time.Millisecond, AdvanceSweep: 500 * time.Millisecond,
}

// clusterSecret is the HMAC secret every chaos controller shares.
const clusterSecret = "chaos-cluster-secret-0123456789abcdef"

const (
	leaseTTL    = 1500 * time.Millisecond
	waitTimeout = 30 * time.Second
	jobSpec     = `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"chaos"},
"spec":{"steps":[{"id":"s1","plugin":"gate","with":{"word":"one"}},{"id":"s2","plugin":"gate","with":{"word":"two"}}]}}`
)

// controllerNode is one in-process controller with its own store and listener.
type controllerNode struct {
	id   string
	st   store.Store
	node *node.Node
	srv  *httptest.Server
	once sync.Once
}

// stop shuts the controller down gracefully, once; the test cleanup calls it
// too, so a scenario may stop a controller itself.
func (cn *controllerNode) stop() error {
	var err error
	cn.once.Do(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cn.srv.Close()
		err = cn.node.Stop(sctx)
	})
	return err
}

// Cluster is N controllers on one Postgres database, plus routable agents.
type Cluster struct {
	t     *testing.T
	ctls  []*controllerNode
	lb    *lb
	token string
	jv    store.JobVersion
	dsn   string
	read  *postgres.Store // assertions read through it, whatever happens to the controllers' stores
}

// newCluster starts n controllers (ids ctl-A, ctl-B, ...) on a fresh database
// with pool "default" and one job.
func newCluster(t *testing.T, n int) *Cluster {
	t.Helper()
	c := newEmptyCluster(t)
	for range n {
		c.addController(timing, nil)
	}
	return c
}

// newEmptyCluster migrates a fresh database and seeds it; the test adds
// controllers with addController.
func newEmptyCluster(t *testing.T) *Cluster {
	t.Helper()
	ctx := context.Background()
	c := &Cluster{t: t, lb: newLB(), dsn: newPGDatabase(t)}
	rd, err := postgres.Open(ctx, c.dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rd.Close() })
	if err := rd.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	c.read = rd
	c.seed(rd)
	return c
}

// addController starts the next controller (ctl-A, ctl-B, ...) with the given
// timing. wrap, if set, puts a decorator around its store; the decorator must
// keep the embedded store's Pool method so the queue still finds Postgres.
func (c *Cluster) addController(tm node.Timing, wrap func(*postgres.Store) store.Store) *controllerNode {
	c.t.Helper()
	pg, err := postgres.Open(context.Background(), c.dsn)
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(func() { _ = pg.Close() })
	var st store.Store = pg
	if wrap != nil {
		st = wrap(pg)
	}
	cn := c.startController(st, "ctl-"+string(rune('A'+len(c.ctls))), tm)
	c.ctls = append(c.ctls, cn)
	return cn
}

func (c *Cluster) seed(st store.Store) {
	ctx := context.Background()
	now := time.Now()
	pool, err := st.CreatePool(ctx, "default", "", now)
	if err != nil {
		c.t.Fatal(err)
	}
	plain, hash, err := tokens.New()
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := st.CreatePoolToken(ctx, pool.ID, "t", hash, now); err != nil {
		c.t.Fatal(err)
	}
	c.token = plain
	_, c.jv, err = st.CreateJob(ctx, store.NewJob{ProjectID: store.DefaultProjectID, Name: "chaos", Spec: json.RawMessage(jobSpec), Checksum: "sum"}, now)
	if err != nil {
		c.t.Fatal(err)
	}
}

func (c *Cluster) startController(st store.Store, id string, tm node.Timing) *controllerNode {
	c.t.Helper()
	log := slog.New(slog.DiscardHandler)
	reg, err := pluginreg.New(nil, log)
	if err != nil {
		c.t.Fatal(err)
	}
	// Controllers reach each other's internal listener (log tail proxy) on a
	// real port, bound first so its address can be registered.
	iln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		c.t.Fatal(err)
	}
	n, err := node.Start(context.Background(), node.Config{
		Store: st, ControllerID: id, InternalURL: "http://" + iln.Addr().String(), Plugins: reg, InsecureDev: true,
		ClusterSecret: clusterSecret, InternalListener: iln,
		Timing: tm, Logger: log,
	})
	if err != nil {
		c.t.Fatal(err)
	}
	cn := &controllerNode{id: id, st: st, node: n, srv: httptest.NewServer(n.Handler())}
	c.t.Cleanup(func() { _ = cn.stop() })
	c.lb.targets[id] = strings.TrimPrefix(cn.srv.URL, "http://")
	return cn
}

// addAgent starts an agent that dials through the load balancer, initially
// routed to controller idx.
func (c *Cluster) addAgent(name string, idx int, plugins ...pluginsdk.Plugin) *agent.Agent {
	c.t.Helper()
	c.lb.route(name, c.ctls[idx].id)
	ag, err := agent.New(agent.Config{
		ControllerURL: "ws://lb.chaos.invalid", Token: c.token, AgentID: name, Pool: "default", Slots: 2,
		Plugins: plugins, Logger: slog.New(slog.DiscardHandler), HeartbeatInterval: 50 * time.Millisecond,
		HTTPClient: &http.Client{Transport: &http.Transport{DialContext: c.lb.dialer(name)}},
		ExecConfig: agent.ExecConfig{SpoolDir: c.t.TempDir(), WorkRoot: c.t.TempDir(), LogFlushInterval: 10 * time.Millisecond, CompleteRetry: 50 * time.Millisecond},
	})
	if err != nil {
		c.t.Fatal(err)
	}
	if err := ag.Start(context.Background()); err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = ag.Stop(sctx)
	})
	return ag
}

// trigger creates a run through controller idx the way the API does.
func (c *Cluster) trigger(idx int) string { return c.triggerPlugin(idx, "gate") }

// triggerPlugin is trigger with step s1 running the given plugin.
func (c *Cluster) triggerPlugin(idx int, plugin string) string {
	return c.triggerWord(idx, plugin, "one")
}

// triggerWord is triggerPlugin with the step's "word" input.
func (c *Cluster) triggerWord(idx int, plugin, word string) string {
	c.t.Helper()
	ctl := c.ctls[idx]
	var id string
	err := ctl.st.WithTx(context.Background(), func(tx store.Tx) error {
		tree, err := tx.CreateRun(context.Background(), store.NewRun{
			JobID: c.jv.JobID, JobVersionID: c.jv.ID, Trigger: json.RawMessage(`{"type":"manual"}`), Options: json.RawMessage(`{}`),
			Steps: []store.NewStepInstance{{Path: "s1", Plugin: plugin, Input: json.RawMessage(`{"word":"` + word + `"}`),
				Execution: &store.NewExecution{Pool: advance.DefaultPool, ScheduledAt: time.Now()}}},
		}, time.Now())
		if err != nil {
			return err
		}
		id = tree.ID
		return ctl.node.Advancer().Enqueue(context.Background(), tx, id)
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return id
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// step returns the first step instance of the run at path, with its executions.
func (c *Cluster) step(runID, path string) (store.StepNode, bool) {
	tree, err := c.read.GetRunTree(context.Background(), runID)
	if err != nil {
		c.t.Fatal(err)
	}
	for _, s := range tree.Steps {
		if s.Path == path {
			return s, true
		}
	}
	return store.StepNode{}, false
}

// lb routes agent connections to controllers. It tracks the live connections
// per agent so a test can cut them, and refuses to connect an unrouted agent.
type lb struct {
	mu      sync.Mutex
	targets map[string]string // controller id -> host:port
	routes  map[string]string // agent -> controller id; "" means unreachable
	conns   map[string][]net.Conn
}

func newLB() *lb {
	return &lb{targets: map[string]string{}, routes: map[string]string{}, conns: map[string][]net.Conn{}}
}

func (l *lb) route(agent, ctl string) {
	l.mu.Lock()
	l.routes[agent] = ctl
	l.mu.Unlock()
}

// sever closes every live connection of the agent.
func (l *lb) sever(agent string) {
	l.mu.Lock()
	conns := l.conns[agent]
	l.conns[agent] = nil
	l.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (l *lb) dialer(agent string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		l.mu.Lock()
		target := l.targets[l.routes[agent]]
		l.mu.Unlock()
		if target == "" {
			return nil, errors.New("chaos: agent is not routed to a controller")
		}
		var d net.Dialer
		conn, err := d.DialContext(ctx, network, target)
		if err != nil {
			return nil, err
		}
		l.mu.Lock()
		l.conns[agent] = append(l.conns[agent], conn)
		l.mu.Unlock()
		return conn, nil
	}
}
