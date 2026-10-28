package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Agent keeps one connection to the controller alive. Create it with New,
// then Start it; Stop ends the connection and waits for its goroutines.
type Agent struct {
	cfg     Config
	log     *slog.Logger
	connURL string

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
	fatal   error

	connected atomic.Bool
	draining  atomic.Bool
}

// New validates cfg. Nothing runs until Start.
func New(cfg Config) (*Agent, error) {
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}
	u, err := connectURL(cfg.ControllerURL)
	if err != nil {
		return nil, err
	}
	return &Agent{
		cfg: cfg, connURL: u, done: make(chan struct{}),
		log: cfg.Logger.With("agent_id", cfg.AgentID),
	}, nil
}

// Start launches the connection loop. It returns immediately; ctx cancellation
// ends the loop like Stop does.
func (a *Agent) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return errors.New("agent: already started")
	}
	a.started = true
	ctx, a.cancel = context.WithCancel(ctx)
	go a.run(ctx)
	a.log.Info("agent started", "pool", a.cfg.Pool, "slots", a.cfg.Slots)
	return nil
}

// Stop ends the loop, closes the connection and waits for the goroutines
// until ctx expires. It is safe to call without Start and more than once.
func (a *Agent) Stop(ctx context.Context) error {
	a.mu.Lock()
	started, cancel := a.started, a.cancel
	a.mu.Unlock()
	if !started {
		return nil
	}
	cancel()
	select {
	case <-a.done:
		a.log.Info("agent stopped")
		return nil
	case <-ctx.Done():
		return fmt.Errorf("waiting for agent to stop: %w", ctx.Err())
	}
}

// Done is closed when the connection loop has ended: after Stop, or when the
// controller refused the agent in a way retrying cannot fix (see Err).
func (a *Agent) Done() <-chan struct{} { return a.done }

// Err returns the fatal error that ended the loop, or nil.
func (a *Agent) Err() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.fatal
}

// Connected reports whether a hello has been accepted on the live socket.
func (a *Agent) Connected() bool { return a.connected.Load() }

// Draining reports whether the controller asked the agent to stop taking work.
func (a *Agent) Draining() bool { return a.draining.Load() }

func (a *Agent) run(ctx context.Context) {
	defer close(a.done)
	var seq uint64
	attempt := 0
	for ctx.Err() == nil {
		seq++ // strictly higher than every earlier hello of this process
		out := a.session(ctx, seq)
		a.connected.Store(false)
		if ctx.Err() != nil {
			return
		}
		if out.fatal != nil {
			a.log.Error("controller refused the agent; not retrying", "error", out.fatal)
			a.mu.Lock()
			a.fatal = out.fatal
			a.mu.Unlock()
			return
		}
		wait := a.nextWait(out, &attempt)
		a.log.Info("disconnected; reconnecting", "wait", wait.String(), "reason", out.reason())
		if !a.sleep(ctx, wait) {
			return
		}
	}
}

// nextWait chooses the delay before the next connection attempt. A requested
// reconnect waits the controller's jitter and resets the backoff; a session
// that stayed up for a heartbeat interval restarts the backoff; anything else backs off.
func (a *Agent) nextWait(out outcome, attempt *int) time.Duration {
	switch {
	case out.reconnect != nil:
		*attempt = 0
		return *out.reconnect
	case out.stable:
		*attempt = 0
	}
	d := BackoffDelay(*attempt, a.cfg.BackoffBase, a.cfg.BackoffMax, a.cfg.Rand())
	*attempt++
	return d
}

// sleep waits d; it reports false if ctx ended first.
func (a *Agent) sleep(ctx context.Context, d time.Duration) bool {
	t := a.cfg.Clock.timer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return true
	case <-ctx.Done():
		return false
	}
}
