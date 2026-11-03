package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/denyszorinets/damson/internal/controller"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/store"
)

// Defaults for Config zero values.
const (
	DefaultDispatchInterval = time.Second      // ARCHITECTURE 8.3: notify hint or 1s
	DefaultRenewInterval    = 10 * time.Second // ARCHITECTURE 7.3: one UPDATE per controller every 10s
	DefaultReapInterval     = 10 * time.Second
	DefaultCancelInterval   = 2 * time.Second // ARCHITECTURE 8.3: cancel poll until a notify hint exists
	DefaultCancelGrace      = 5 * time.Second // ARCHITECTURE 7.3: the agent's default grace
	DefaultIncapableRetry   = 5 * time.Second // how long an execution claimed by an agent that cannot run it waits
	DefaultLeaseTTL         = time.Minute
	DefaultCallTimeout      = 10 * time.Second // one step.execute round trip or store call
	reapBatch               = 100
)

// Store is the part of the store the dispatcher uses.
type Store interface {
	store.ExecutionStore
	store.TxRunner
	store.LeaderStore
	GetStepInstance(ctx context.Context, id string) (store.StepInstance, error)
}

// LeaderReaper is the name of the lease of the reaper duty.
const LeaderReaper = "reaper"

// DeadControllerAfter is how long a controller instance may stay silent before
// the leader removes its registry row.
const DeadControllerAfter = time.Minute

// Leader reports whether this controller holds the lease of a duty, and its term.
type Leader interface {
	Term() (term int64, ok bool)
}

// Agents is the controller's live sockets.
type Agents interface {
	Agents() []controller.AgentView
	Reserve(agentID string, n int) int
	Release(agentID string, n int)
}

// Resolver finds the plugin version and the capable agents for a step.
type Resolver interface {
	Resolve(ref string, f pluginreg.Filter) (pluginreg.Resolution, error)
}

// Enqueuer queues run.advance in the caller's transaction.
type Enqueuer interface {
	Enqueue(ctx context.Context, tx store.Tx, runID string) error
}

// Config configures a Dispatcher.
type Config struct {
	Store   Store
	Agents  Agents
	Plugins Resolver
	// Nodes and Secrets fill the nodes and secret bundle of step.execute.
	// Both are optional: without them a step gets neither (ADR-0034 until M4).
	Nodes        NodeSource
	Secrets      SecretSource
	Advance      Enqueuer
	ControllerID string
	// Leader gates the reaper and the dead-controller cleanup to the holder of
	// the LeaderReaper lease. Nil means every controller runs them (they are
	// idempotent, so this is only wasteful).
	Leader Leader
	Now    func() time.Time
	Logger *slog.Logger
	// Invalidate drops a cached attempt lookup (logs.Ingest.Invalidate). It is
	// called for the attempt of an execution that was requeued. Optional.
	Invalidate func(attemptID string)
	// Finish flushes the logs of an execution that completed and releases its
	// live tail (logs.Ingest.Finish). Called after the completion commits;
	// a failure is logged and left to the ingest's own flush loop. Optional.
	Finish func(ctx context.Context, executionID string) error

	// Zero values select the Default* constants.
	DispatchInterval time.Duration
	RenewInterval    time.Duration
	ReapInterval     time.Duration
	CancelInterval   time.Duration
	CancelGrace      time.Duration // grace_ms of step.cancel
	// IncapableRetry delays an execution claimed by an agent that cannot run it
	// before it is claimed again, so a step nobody can run does not spin the claim
	// loop (known limitation: incapable agents in the pool churn it).
	IncapableRetry time.Duration
	LeaseTTL       time.Duration
	CallTimeout    time.Duration
}

// Dispatcher owns the dispatch, renewal and reaper loops. Register it as a
// controller.SessionHook for step.complete.
type Dispatcher struct {
	cfg Config
	log *slog.Logger

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	busy    map[string]bool // agents with a dispatch or cancel round in flight
	// cancelled holds the attempts whose step.cancel an agent has answered, so
	// a flagged attempt is not asked again every tick. It is only a cache:
	// losing it causes a resend, which the agent treats as a no-op.
	cancelled map[string]bool
}

// New validates cfg and applies defaults.
func New(cfg Config) (*Dispatcher, error) {
	switch {
	case cfg.Store == nil || cfg.Agents == nil || cfg.Plugins == nil || cfg.Advance == nil:
		return nil, errors.New("dispatch: Store, Agents, Plugins and Advance are required")
	case cfg.ControllerID == "":
		return nil, errors.New("dispatch: ControllerID is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	for _, d := range []struct {
		v *time.Duration
		d time.Duration
	}{
		{&cfg.DispatchInterval, DefaultDispatchInterval}, {&cfg.RenewInterval, DefaultRenewInterval},
		{&cfg.ReapInterval, DefaultReapInterval}, {&cfg.CancelInterval, DefaultCancelInterval}, {&cfg.CancelGrace, DefaultCancelGrace}, {&cfg.IncapableRetry, DefaultIncapableRetry},
		{&cfg.LeaseTTL, DefaultLeaseTTL}, {&cfg.CallTimeout, DefaultCallTimeout},
	} {
		if *d.v <= 0 {
			*d.v = d.d
		}
	}
	return &Dispatcher{cfg: cfg, log: cfg.Logger, busy: map[string]bool{}, cancelled: map[string]bool{}}, nil
}

// Start launches the loops. Their lifetime is Stop, not ctx cancellation.
func (d *Dispatcher) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.started {
		return errors.New("dispatch: already started")
	}
	d.started = true
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	d.cancel = cancel
	for _, l := range []struct {
		every time.Duration
		fn    func(context.Context)
	}{
		{d.cfg.DispatchInterval, d.dispatchOnce}, {d.cfg.RenewInterval, d.renewOnce}, {d.cfg.ReapInterval, d.reapOnce},
		{d.cfg.CancelInterval, d.cancelOnce},
	} {
		d.wg.Add(1)
		go func() { defer d.wg.Done(); loop(loopCtx, l.every, l.fn) }()
	}
	return nil
}

// Stop ends the loops and waits for them until ctx expires.
func (d *Dispatcher) Stop(ctx context.Context) error {
	d.mu.Lock()
	cancel := d.cancel
	d.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("waiting for dispatch loops: %w", ctx.Err())
	}
}

func loop(ctx context.Context, every time.Duration, fn func(context.Context)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}
