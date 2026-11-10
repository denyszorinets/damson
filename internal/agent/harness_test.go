package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// testPlugin is a plugin whose behavior a test supplies.
type testPlugin struct {
	name     string
	version  string
	validate func(ctx context.Context, input json.RawMessage) error
	exec     func(ctx context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error)
}

func (p testPlugin) Manifest() pluginsdk.Manifest {
	v := p.version
	if v == "" {
		v = "1.0.0"
	}
	return pluginsdk.Manifest{
		Name: p.name, Version: v, Title: p.name, TargetMode: pluginsdk.TargetNone,
		InputSchema: pluginsdk.MustSchemaFor[struct{}](), OutputSchema: pluginsdk.MustSchemaFor[struct{}](),
	}
}

func (p testPlugin) Validate(ctx context.Context, in json.RawMessage) error {
	if p.validate != nil {
		return p.validate(ctx, in)
	}
	return nil
}

func (p testPlugin) Execute(ctx context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
	return p.exec(ctx, req, emit)
}

// plug builds a plugin named "demo" version 1.0.0 around exec.
func plug(exec func(ctx context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error)) testPlugin {
	return testPlugin{name: "demo", exec: exec}
}

// startAgent starts an agent against the fake controller and waits until the
// controller has seen its hello.
func startAgent(t *testing.T, ctl *fakeCtl, plugins []pluginsdk.Plugin, opts ...opt) (*Agent, *syncBuf) {
	t.Helper()
	logs := &syncBuf{}
	cfg := Config{
		ControllerURL: ctl.url(), Token: "tok", AgentID: "agent-x", Pool: "default", Slots: 4,
		Logger:      slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Sampler:     &fakeSampler{},
		BackoffBase: 5 * time.Millisecond, BackoffMax: 40 * time.Millisecond,
		LookPath: func(string) (string, error) { return "", errors.New("none") },
		Plugins:  plugins,
	}
	cfg.SpoolDir, cfg.WorkRoot = t.TempDir(), t.TempDir()
	cfg.LogFlushInterval, cfg.CompleteRetry = 10*time.Millisecond, 25*time.Millisecond
	for _, o := range opts {
		o(&cfg)
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.Stop(ctx); err != nil {
			t.Errorf("stopping agent: %v", err)
		}
	})
	eventually(t, "agent connected", a.Connected)
	return a, logs
}

func stepReq(attempt string) protocol.StepExecute {
	return protocol.StepExecute{
		AttemptID: attempt, ExecutionID: "ex-" + attempt, StepPath: "0",
		PluginName: "demo", PluginVersion: "1.0.0",
	}
}

func completeOf(t *testing.T, ctl *fakeCtl, attempt string) protocol.StepComplete {
	t.Helper()
	var got protocol.StepComplete
	eventually(t, "step.complete for "+attempt, func() bool {
		for _, c := range ctl.completeList() {
			if c.AttemptID == attempt {
				got = c
				return true
			}
		}
		return false
	})
	return got
}

// blocker is a plugin body that signals it started and waits for ctx or release.
type blocker struct {
	started chan struct{}
	release chan struct{}
	ignore  bool // keep running after ctx is cancelled
}

func newBlocker() *blocker {
	return &blocker{started: make(chan struct{}, 16), release: make(chan struct{})}
}

func (b *blocker) run(ctx context.Context, _ pluginsdk.ExecRequest, _ pluginsdk.Emitter) (pluginsdk.Result, error) {
	b.started <- struct{}{}
	if b.ignore {
		<-b.release
		return pluginsdk.Result{}, nil
	}
	select {
	case <-ctx.Done():
		return pluginsdk.Result{Error: pluginsdk.AsStepError(ctx.Err())}, nil
	case <-b.release:
		return pluginsdk.Result{}, nil
	}
}

func (b *blocker) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-b.started:
	case <-time.After(5 * time.Second):
		t.Fatal("plugin did not start")
	}
}
