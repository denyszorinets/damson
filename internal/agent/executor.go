package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

type pluginKey struct{ name, version string }

// Executor runs steps for the controller: it accepts step.execute within the
// free slots, runs plugins, streams their logs and delivers step.complete
// until acknowledged. It outlives connections; the session attaches the
// current peer after each accepted hello. Create it with newExecutor, then
// Start it; Stop cancels running steps and waits for every goroutine.
type Executor struct {
	cfg      *Config
	log      *slog.Logger
	link     *link
	hub      *logHub
	plugins  map[pluginKey]pluginsdk.Plugin
	draining func() bool
	wake     chan struct{}

	mu      sync.Mutex
	runs    map[string]*run
	done    map[string]*finishedStep // completed, step.complete not yet acknowledged
	tombs   tombstones
	busy    int // plugin goroutines still running, including abandoned ones
	started bool
	stopped bool
	base    context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func newExecutor(cfg *Config, log *slog.Logger, draining func() bool) (*Executor, error) {
	plugins := make(map[pluginKey]pluginsdk.Plugin, len(cfg.Plugins))
	for _, p := range cfg.Plugins {
		m := p.Manifest()
		k := pluginKey{m.Name, m.Version}
		if _, dup := plugins[k]; dup {
			return nil, fmt.Errorf("agent: plugin %s@%s is configured twice", m.Name, m.Version)
		}
		plugins[k] = p
	}
	l := &link{}
	hub := newLogHub(log, l, cfg.MemBufferBytes, cfg.SpoolBytes, cfg.SpoolDir)
	hub.batchBytes.Store(cfg.LogBatchBytes)
	return &Executor{
		cfg: cfg, log: log, link: l, hub: hub, plugins: plugins, draining: draining,
		wake: make(chan struct{}, 1),
		runs: map[string]*run{}, done: map[string]*finishedStep{},
	}, nil
}

// Start launches the delivery loop. ctx ends it like Stop does.
func (e *Executor) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return errors.New("agent: executor already started")
	}
	e.started = true
	e.base, e.cancel = context.WithCancel(ctx)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.completeLoop(e.base)
	}()
	return nil
}

// Stop cancels running steps and waits for the goroutines until ctx expires.
// Unacknowledged results and logs are lost; that is the stateless-agent
// trade-off of ARCHITECTURE.md section 7.3. Safe to call without Start.
func (e *Executor) Stop(ctx context.Context) error {
	e.mu.Lock()
	if !e.started || e.stopped {
		e.mu.Unlock()
		return nil
	}
	e.stopped = true
	e.cancel()
	e.mu.Unlock()

	waited := make(chan struct{})
	go func() { e.wg.Wait(); close(waited) }()
	select {
	case <-waited:
		e.hub.close()
		return nil
	case <-ctx.Done():
		return fmt.Errorf("waiting for steps to stop: %w", ctx.Err())
	}
}

// spawn runs fn in a goroutine owned by the executor. It reports false when
// the executor is not running. Callers must not hold e.mu.
func (e *Executor) spawn(fn func()) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.spawnLocked(fn)
}

func (e *Executor) spawnLocked(fn func()) bool {
	if !e.started || e.stopped {
		return false
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		fn()
	}()
	return true
}

// HandleExecute answers step.execute: accepted, or rejected with a reason.
// An accepted step runs in its own goroutine.
func (e *Executor) HandleExecute(_ context.Context, p protocol.Params) (any, error) {
	var req protocol.StepExecute
	if err := p.Decode(&req); err != nil {
		return nil, err
	}
	if req.AttemptID == "" {
		return nil, &protocol.RPCError{Code: protocol.CodeInvalidParams, Message: "attempt_id is required"}
	}
	return e.accept(req), nil
}

func (e *Executor) accept(req protocol.StepExecute) protocol.StepExecuteResult {
	reject := func(format string, args ...any) protocol.StepExecuteResult {
		reason := fmt.Sprintf(format, args...)
		e.log.Info("step rejected", "attempt_id", req.AttemptID, "execution_id", req.ExecutionID, "reason", reason)
		return protocol.StepExecuteResult{Reason: reason}
	}
	plug, known := e.plugins[pluginKey{req.PluginName, req.PluginVersion}]

	e.mu.Lock()
	defer e.mu.Unlock()
	_, running := e.runs[req.AttemptID]
	_, finished := e.done[req.AttemptID]
	switch {
	case running || finished: // a retried dispatch: already ours
		return protocol.StepExecuteResult{Accepted: true}
	case !e.started || e.stopped:
		return reject("agent is stopping")
	case e.draining != nil && e.draining():
		return reject("draining")
	case e.tombs.has(req.AttemptID, e.cfg.Now()):
		return reject("cancelled before start")
	case !known:
		return reject("unknown plugin %s@%s", req.PluginName, req.PluginVersion)
	case e.busy >= e.cfg.Slots:
		return reject("no free slot")
	}
	r := e.newRun(req)
	e.runs[req.AttemptID] = r
	e.busy++
	e.spawnLocked(func() { e.execute(r, req, plug) })
	return protocol.StepExecuteResult{Accepted: true}
}

// HandleCancel answers step.cancel. A cancel for an attempt that has not
// arrived yet is remembered, and the late step.execute is refused.
func (e *Executor) HandleCancel(_ context.Context, p protocol.Params) (any, error) {
	var c protocol.StepCancel
	if err := p.Decode(&c); err != nil {
		return nil, err
	}
	grace := time.Duration(c.GraceMS) * time.Millisecond
	if grace <= 0 {
		grace = e.cfg.CancelGrace
	}
	e.mu.Lock()
	r := e.runs[c.AttemptID]
	if _, finished := e.done[c.AttemptID]; r == nil && !finished {
		e.tombs.add(c.AttemptID, e.cfg.Now())
	}
	e.mu.Unlock()
	if r != nil {
		e.log.Info("canceling step", "attempt_id", c.AttemptID, "grace", grace.String())
		r.requestCancel(grace)
	}
	return protocol.StepCancelResult{Known: r != nil}, nil
}

// HandleLogAck frees acknowledged log batches.
func (e *Executor) HandleLogAck(_ context.Context, p protocol.Params) (any, error) {
	var a protocol.LogAck
	if err := p.Decode(&a); err != nil {
		return nil, err
	}
	e.hub.ack(a.AttemptID, a.Seq)
	return nil, nil
}

// Running returns the running attempt ids, sorted, for heartbeats.
func (e *Executor) Running() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	ids := make([]string, 0, len(e.runs))
	for id := range e.runs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// FreeSlots is what heartbeats report: zero while draining, never negative.
func (e *Executor) FreeSlots() int {
	if e.draining != nil && e.draining() {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return max(e.cfg.Slots-e.busy, 0)
}

// SpoolBytes is the log spool size reported in heartbeats.
func (e *Executor) SpoolBytes() int64 { return e.hub.spoolBytes() }

// releaseSlot frees a slot when a plugin goroutine has returned.
func (e *Executor) releaseSlot() {
	e.mu.Lock()
	e.busy--
	e.mu.Unlock()
}
