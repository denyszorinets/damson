package dispatch_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/agent"
	"github.com/denyszorinets/damson/internal/controller"
	"github.com/denyszorinets/damson/internal/controller/advance"
	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/controller/logs"
	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/expr"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

var t0 = time.Now().UTC()

const twoStepSpec = `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"lin"},
"spec":{"steps":[{"id":"s1","plugin":"demo","with":{"word":"one"}},{"id":"s2","plugin":"demo","with":{"word":"two"}}]}}`

// demoPlugin logs its "word" input and fails when it is "boom".
type demoPlugin struct{}

func (demoPlugin) Manifest() pluginsdk.Manifest {
	return pluginsdk.Manifest{
		Name: "demo", Version: "1.0.0", Title: "demo", TargetMode: pluginsdk.TargetNone,
		InputSchema: pluginsdk.MustSchemaFor[struct{}](), OutputSchema: pluginsdk.MustSchemaFor[struct{}](),
	}
}

func (demoPlugin) Validate(context.Context, json.RawMessage) error { return nil }

func (demoPlugin) Execute(ctx context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
	var in struct{ Word string }
	_ = json.Unmarshal(req.Input, &in)
	emit.Log(pluginsdk.Stdout, "said "+in.Word)
	if in.Word == "slow" {
		select {
		case <-time.After(1200 * time.Millisecond):
		case <-ctx.Done():
		}
	}
	if in.Word == "boom" {
		return pluginsdk.Result{Error: &pluginsdk.StepError{Message: "boom"}}, nil
	}
	return pluginsdk.Result{Outputs: json.RawMessage(`{}`)}, nil
}

// cluster is a controller (gateway, advancer, ingest, dispatcher) with one real agent.
type cluster struct {
	t   *testing.T
	st  store.Store
	adv *advance.Advancer
	jv  store.JobVersion
	dsp *dispatch.Dispatcher
}

func startCluster(t *testing.T, st store.Store, spec string, opts ...func(*dispatch.Config)) *cluster {
	t.Helper()
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	pool, err := st.CreatePool(ctx, "default", "", t0)
	if err != nil {
		t.Fatal(err)
	}
	plain, hash, err := tokens.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePoolToken(ctx, pool.ID, "t", hash, t0); err != nil {
		t.Fatal(err)
	}
	reg, err := pluginreg.New(nil, log)
	if err != nil {
		t.Fatal(err)
	}
	gw, err := controller.New(controller.Config{
		Store: st, Plugins: reg, ControllerID: "ctl-1", InsecureDev: true, Logger: log,
		HeartbeatInterval: 50 * time.Millisecond, FlushInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(expr.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	adv, err := advance.New(advance.Config{Store: st, Engine: eng, PollInterval: 20 * time.Millisecond, SweepInterval: -1, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	ing, err := logs.New(logs.Config{Store: st, Logger: log, FlushInterval: 50 * time.Millisecond, CheckInterval: 20 * time.Millisecond,
		Acks: func(ctx context.Context, id string, ack protocol.LogAck) error {
			p, ok := gw.Peer(id)
			if !ok {
				return nil
			}
			return p.Notify(ctx, protocol.MethodLogAck, ack)
		}})
	if err != nil {
		t.Fatal(err)
	}
	dcfg := dispatch.Config{
		Store: st, Agents: gw, Plugins: reg, Advance: adv, ControllerID: "ctl-1", Logger: log,
		DispatchInterval: 20 * time.Millisecond, RenewInterval: 50 * time.Millisecond, ReapInterval: 50 * time.Millisecond,
	}
	for _, o := range opts {
		o(&dcfg)
	}
	dsp, err := dispatch.New(dcfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []controller.SessionHook{ing, dsp} {
		if err := gw.AddHook(h); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	mux.Handle(controller.ConnectPath, gw.Handler())
	srv := httptest.NewServer(mux)
	for _, start := range []func(context.Context) error{adv.Start, ing.Start, dsp.Start, gw.Start} {
		if err := start(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = dsp.Stop(sctx)
		_ = gw.Stop(sctx)
		srv.Close()
		_ = ing.Stop(sctx)
		_ = adv.Stop(sctx)
	})

	ag, err := agent.New(agent.Config{
		ControllerURL: "ws" + strings.TrimPrefix(srv.URL, "http"), Token: plain, AgentID: "agent-1",
		Pool: "default", Slots: 2, Plugins: []pluginsdk.Plugin{demoPlugin{}}, Logger: log, HeartbeatInterval: 50 * time.Millisecond,
		ExecConfig: agent.ExecConfig{SpoolDir: t.TempDir(), WorkRoot: t.TempDir(), LogFlushInterval: 10 * time.Millisecond, CompleteRetry: 50 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = ag.Stop(sctx)
	})

	_, jv, err := st.CreateJob(ctx, store.NewJob{ProjectID: store.DefaultProjectID, Name: "lin", Spec: json.RawMessage(spec), Checksum: "sum"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	return &cluster{t: t, st: st, adv: adv, jv: jv, dsp: dsp}
}

// trigger creates a run the way the API does: the first step queued.
func (c *cluster) trigger(word string) string { return c.triggerPlugin("demo", word) }

func (c *cluster) triggerPlugin(plugin, word string) string {
	c.t.Helper()
	var id string
	err := c.st.WithTx(context.Background(), func(tx store.Tx) error {
		tree, err := tx.CreateRun(context.Background(), store.NewRun{
			JobID: c.jv.JobID, JobVersionID: c.jv.ID, Trigger: json.RawMessage(`{"type":"manual"}`), Options: json.RawMessage(`{}`),
			Steps: []store.NewStepInstance{{Path: "s1", Plugin: plugin, Input: json.RawMessage(`{"word":"` + word + `"}`),
				Execution: &store.NewExecution{Pool: advance.DefaultPool, ScheduledAt: time.Now()}}},
		}, time.Now())
		if err != nil {
			return err
		}
		id = tree.ID
		return c.adv.Enqueue(context.Background(), tx, id)
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return id
}

func (c *cluster) waitRun(id string, want store.RunStatus) store.RunTree {
	c.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		tree, err := c.st.GetRunTree(context.Background(), id)
		if err != nil {
			c.t.Fatal(err)
		}
		if tree.Status.Terminal() || time.Now().After(deadline) {
			if tree.Status != want {
				c.t.Fatalf("run status = %s; want %s (steps %+v)", tree.Status, want, tree.Steps)
			}
			return tree
		}
		time.Sleep(20 * time.Millisecond)
	}
}
