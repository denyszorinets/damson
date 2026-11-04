// Package leader elects one controller per singleton duty through a lease row
// in the database (ARCHITECTURE section 8.4). Leadership is an optimization
// and a single clock for expiry decisions, never a correctness requirement:
// duties stay idempotent, and the term fences a deposed leader's last action.
package leader

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

// Defaults of Config.
const (
	DefaultTTL   = 15 * time.Second
	DefaultRenew = 5 * time.Second
)

// Config configures an Elector.
type Config struct {
	Store store.LeaderStore
	// Name is the duty ("reaper"); Holder is this controller's id.
	Name, Holder string
	// TTL is how long an acquired or renewed lease lasts; Renew is how often it
	// is renewed (or taken). Zero values select the defaults. Renew should be at
	// most a third of TTL.
	TTL, Renew time.Duration
	Now        func() time.Time
	Logger     *slog.Logger
}

// Elector holds or chases one leader lease. Start launches one goroutine, Stop
// ends it and releases the lease.
type Elector struct {
	cfg Config
	log *slog.Logger

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
	term    int64
	expires time.Time // zero when not leader
}

// New validates cfg.
func New(cfg Config) (*Elector, error) {
	if cfg.Store == nil || cfg.Name == "" || cfg.Holder == "" {
		return nil, errors.New("leader: store, name and holder are required")
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.Renew <= 0 {
		cfg.Renew = DefaultRenew
	}
	if cfg.Renew >= cfg.TTL {
		return nil, fmt.Errorf("leader: renew interval %s must be shorter than the ttl %s", cfg.Renew, cfg.TTL)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Elector{cfg: cfg, log: log.With("lease", cfg.Name, "holder", cfg.Holder)}, nil
}

// Term returns the term and true while this controller leads. It stops
// reporting leadership one renew interval before the lease expires, so a
// controller that cannot reach the database steps down before another can
// take over.
func (e *Elector) Term() (int64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.expires.IsZero() || !e.cfg.Now().Before(e.expires.Add(-e.cfg.Renew)) {
		return 0, false
	}
	return e.term, true
}

// Start tries to acquire the lease at once, then every renew interval.
func (e *Elector) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return errors.New("leader: already started")
	}
	e.started = true
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	e.cancel, e.done = cancel, make(chan struct{})
	go e.run(loopCtx, e.done)
	return nil
}

// ErrHeld means another holder's lease was still valid when StartExclusive
// gave up.
var ErrHeld = errors.New("leader: the lease is held by another controller")

// StartExclusive acquires the lease before returning, retrying until wait has
// passed (a lease left by a crashed holder runs out within the ttl), then
// starts renewing it like Start. It returns ErrHeld when the lease is still
// someone else's.
func (e *Elector) StartExclusive(ctx context.Context, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		e.tick(ctx)
		if _, ok := e.Term(); ok {
			return e.Start(ctx)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return ErrHeld
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(250*time.Millisecond, e.cfg.Renew)):
		}
	}
}

func (e *Elector) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	e.tick(ctx)
	t := time.NewTicker(e.cfg.Renew)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.tick(ctx)
		}
	}
}

// tick acquires or renews. A store error keeps the local state, which expires
// on its own; a refusal means another holder has a valid lease.
func (e *Elector) tick(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, e.cfg.TTL-e.cfg.Renew)
	defer cancel()
	now := e.cfg.Now()
	lease, ok, err := e.cfg.Store.AcquireLeader(cctx, e.cfg.Name, e.cfg.Holder, now, now.Add(e.cfg.TTL))
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case err != nil:
		if ctx.Err() == nil {
			e.log.Error("renewing leader lease", "error", err)
		}
	case !ok:
		if !e.expires.IsZero() {
			e.log.Warn("leader lease lost", "term", e.term)
		}
		e.expires = time.Time{}
	default:
		if e.expires.IsZero() || lease.Term != e.term {
			e.log.Info("became leader", "term", lease.Term)
		}
		e.term, e.expires = lease.Term, lease.ExpiresAt
	}
}

// Stop ends the goroutine, waits for it until ctx expires and releases the
// lease so another controller takes over without waiting for the ttl.
func (e *Elector) Stop(ctx context.Context) error {
	e.mu.Lock()
	cancel, done := e.cancel, e.done
	e.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
		return fmt.Errorf("waiting for leader elector: %w", ctx.Err())
	}
	e.mu.Lock()
	term, held := e.term, !e.expires.IsZero()
	e.expires = time.Time{}
	e.mu.Unlock()
	if !held {
		return nil
	}
	// Best effort: bounded, so a hung database cannot hold up the shutdown.
	rctx, cancel := context.WithTimeout(ctx, e.cfg.TTL-e.cfg.Renew)
	defer cancel()
	if err := e.cfg.Store.ReleaseLeader(rctx, e.cfg.Name, e.cfg.Holder, term, e.cfg.Now()); err != nil {
		return fmt.Errorf("releasing leader lease: %w", err)
	}
	return nil
}
