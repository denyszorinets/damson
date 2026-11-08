// Package logsweep is the controller duty that deletes log objects held
// outside the database (ADR-0067). A purge or an execution claim queues an
// object prefix in the same transaction that drops the chunk rows; every
// controller runs this sweeper, and a compare-and-swap on the queue entry
// gives each entry to one controller at a time. It is not a leader duty.
package logsweep

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/denyszorinets/damson/internal/logblob"
	"github.com/denyszorinets/damson/internal/store"
)

// Defaults for Config zero values.
const (
	DefaultInterval = 30 * time.Second
	DefaultBatch    = 50
	DefaultTimeout  = 60 * time.Second
)

// Config configures a Sweeper.
type Config struct {
	Store store.LogDeleteStore
	// Backends are the external backends whose objects are deleted. With none,
	// entries are left in the queue (a controller without s3 configured must not
	// drop entries for objects that live in s3) and a warning is logged.
	Backends []logblob.Blob
	Interval time.Duration
	Batch    int
	// Timeout bounds the delete of one entry.
	Timeout time.Duration
	Now     func() time.Time
	Logger  *slog.Logger
}

// Sweeper runs the duty.
type Sweeper struct {
	cfg    Config
	log    *slog.Logger
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// New validates cfg and returns a stopped Sweeper.
func New(cfg Config) (*Sweeper, error) {
	if cfg.Store == nil {
		return nil, errors.New("logsweep: a store is required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Batch <= 0 {
		cfg.Batch = DefaultBatch
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Sweeper{cfg: cfg, log: log.With("component", "logsweep")}, nil
}

// Start begins the periodic sweep.
func (s *Sweeper) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return errors.New("logsweep: already started")
	}
	ctx, s.cancel = context.WithCancel(context.WithoutCancel(ctx))
	s.done = make(chan struct{})
	go s.run(ctx, s.done)
	return nil
}

// Stop ends the loop and waits for it.
func (s *Sweeper) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel = nil
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stopping log sweeper: %w", ctx.Err())
	}
}

func (s *Sweeper) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		s.Once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Once sweeps one batch of due entries and returns how many it completed.
func (s *Sweeper) Once(ctx context.Context) int {
	due, err := s.cfg.Store.ListDueLogObjectDeletes(ctx, s.cfg.Now(), s.cfg.Batch)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("listing due log deletes", "error", err)
		}
		return 0
	}
	if len(due) > 0 && len(s.cfg.Backends) == 0 {
		s.log.Warn("log objects are queued for deletion but no external log store is configured on this controller; configure logs.s3 or remove the queue entries", "entries", len(due))
		return 0
	}
	done := 0
	for _, d := range due {
		if ctx.Err() != nil {
			break
		}
		if s.one(ctx, d) {
			done++
		}
	}
	return done
}

func (s *Sweeper) one(ctx context.Context, d store.LogObjectDelete) bool {
	now := s.cfg.Now() // per entry: a batch can outlast the claim lease of its later entries
	// Claim the entry by moving its due time past the delete's timeout and its
	// backoff. A controller that loses the CAS skips it; one that dies mid-delete
	// leaves the entry to be retried after the backoff.
	next := now.Add(s.cfg.Timeout + store.LogDeleteBackoff(d.Tries))
	if err := s.cfg.Store.DeferLogObjectDelete(ctx, d, next); err != nil {
		if !errors.Is(err, store.ErrStale) && ctx.Err() == nil {
			s.log.Warn("claiming a log delete", "error", err)
		}
		return false
	}
	d.NotBefore, d.Tries = store.NormalizeTime(next), d.Tries+1
	if !store.ValidLogDeletePrefix(d.Prefix) {
		s.log.Error("dropping a log delete with an invalid prefix", "id", d.ID)
	} else if err := s.deleteAll(ctx, d.Prefix); err != nil {
		if ctx.Err() == nil {
			s.log.Warn("deleting log objects", "prefix", d.Prefix, "tries", d.Tries, "error", err)
		}
		return false
	}
	if err := s.cfg.Store.CompleteLogObjectDelete(ctx, d); err != nil {
		if !errors.Is(err, store.ErrStale) && ctx.Err() == nil {
			s.log.Warn("completing a log delete", "error", err)
		}
		return false
	}
	return true
}

func (s *Sweeper) deleteAll(ctx context.Context, prefix string) error {
	for _, b := range s.cfg.Backends {
		dctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
		_, err := b.DeletePrefix(dctx, prefix)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}
