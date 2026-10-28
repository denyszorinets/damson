package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// run is one accepted step on this agent.
type run struct {
	attempt   string
	execCtx   context.Context
	cancel    context.CancelFunc
	graceNS   atomic.Int64
	requested atomic.Bool // step.cancel or a reconciliation asked for it
}

// newRun derives the step's context from the executor's. The timeout, if
// any, starts now.
func (e *Executor) newRun(req protocol.StepExecute) *run {
	r := &run{attempt: req.AttemptID}
	if req.TimeoutMS > 0 {
		r.execCtx, r.cancel = context.WithTimeout(e.base, time.Duration(req.TimeoutMS)*time.Millisecond)
	} else {
		r.execCtx, r.cancel = context.WithCancel(e.base)
	}
	return r
}

// requestCancel cancels the step; the plugin has grace to return before the
// agent reports the step cancelled without it.
func (r *run) requestCancel(grace time.Duration) {
	r.graceNS.Store(int64(grace))
	r.requested.Store(true)
	r.cancel()
}

func (r *run) grace(def time.Duration) time.Duration {
	if g := time.Duration(r.graceNS.Load()); g > 0 {
		return g
	}
	return def
}

// outcome of the plugin goroutine.
type pluginOutcome struct {
	result     pluginsdk.Result
	err        error
	validation bool // err came from Validate
	panicked   bool
	abandoned  bool // the plugin ignored cancellation past the grace period
}

// execute runs one step to its step.complete message. It owns the secret
// bundle: the bundle lives in the resolver and the masker, and is wiped when
// the step ends.
func (e *Executor) execute(r *run, req protocol.StepExecute, plug pluginsdk.Plugin) {
	defer r.cancel()
	started := e.cfg.Now()
	m := newMasker(req.Secrets)
	res := newBundleResolver(req.Secrets)
	req.Secrets = nil
	defer res.wipe()

	pipe := newLogPipe(e.base, e.hub, r.attempt, m, e.cfg.Now, e.cfg.LogFlushInterval, e.cfg.Clock)
	pipe.start()
	out := e.runPlugin(r, req, plug, res, m, pipe)
	msg := e.buildComplete(r, out, m, pipe, started)
	pipe.close()
	e.finish(r, msg)
}

// runPlugin runs Validate and Execute on a goroutine and waits for it. After
// cancellation or timeout the plugin has the grace period to return.
func (e *Executor) runPlugin(r *run, req protocol.StepExecute, plug pluginsdk.Plugin, res *bundleResolver, m *masker, pipe *logPipe) pluginOutcome {
	ch := make(chan pluginOutcome, 1)
	if !e.spawn(func() {
		defer e.releaseSlot()
		ch <- e.callPlugin(r, req, plug, res, m, pipe)
	}) {
		e.releaseSlot()
		return pluginOutcome{err: pluginsdk.NewError(pluginsdk.KindCancelled, "agent is stopping", nil)}
	}
	select {
	case out := <-ch:
		return out
	case <-r.execCtx.Done():
	}
	t := e.cfg.Clock.timer(r.grace(e.cfg.CancelGrace))
	defer t.Stop()
	select {
	case out := <-ch:
		return out
	case <-t.C():
		e.log.Warn("plugin ignored cancellation; reporting the step without it", "attempt_id", r.attempt, "plugin", req.PluginName)
		return pluginOutcome{abandoned: true}
	}
}

func (e *Executor) callPlugin(r *run, req protocol.StepExecute, plug pluginsdk.Plugin, res *bundleResolver, m *masker, pipe *logPipe) (out pluginOutcome) {
	defer func() {
		if v := recover(); v != nil {
			// Only the type and stack are logged: a panic value can carry secrets.
			e.log.Error("plugin panicked", "attempt_id", r.attempt, "plugin", req.PluginName,
				"panic_type", fmt.Sprintf("%T", v), "stack", string(debug.Stack()))
			out = pluginOutcome{panicked: true}
		}
	}()
	input, err := json.Marshal(req.Input)
	if err != nil || req.Input == nil {
		input = json.RawMessage(`{}`)
	}
	if err := plug.Validate(r.execCtx, input); err != nil {
		return pluginOutcome{err: err, validation: true}
	}
	dir, err := os.MkdirTemp(e.cfg.WorkRoot, "damson-exec-*")
	if err != nil {
		return pluginOutcome{err: pluginsdk.NewError(pluginsdk.KindInternal, "creating work dir failed", err)}
	}
	defer func() { _ = os.RemoveAll(dir) }()

	log := newMaskedLogger(e.log, m).With("attempt_id", r.attempt, "execution_id", req.ExecutionID, "plugin", req.PluginName)
	xr := pluginsdk.ExecRequest{
		Input: input, Secrets: res, Nodes: req.Nodes, WorkDir: dir, Logger: log,
		HostKeys: &hostKeyReporter{ctx: e.base, link: e.link, attempt: r.attempt, log: log},
		Config:   e.cfg.PluginConfig[req.PluginName],
	}
	emit := &stepEmitter{pipe: pipe, m: m, link: e.link, attempt: r.attempt, ctx: e.base}
	result, err := plug.Execute(r.execCtx, xr, emit)
	return pluginOutcome{result: result, err: err}
}
