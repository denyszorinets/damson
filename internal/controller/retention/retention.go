// Package retention is the controller duty that deletes data past its
// retention (ARCHITECTURE 8.4). Today it prunes the audit trail only, when
// audit.retention_days is set (ADR-0054).
//
// The duty is a singleton (ARCHITECTURE 8.4): only the holder of the
// "retention" lease prunes, and each batch is fenced by the lease term. The
// cutoff therefore comes from one controller's clock at a time, and a
// controller with a wrong clock cannot prune unless it holds the lease.
package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

// Actor is the audit actor of the duty's own events.
const Actor = "system:retention"

// batch is how many events one delete removes, so a large backlog never holds
// a long transaction.
const batch = 200

// maxBatches bounds one pass; the rest waits for the next tick.
const maxBatches = 500

// LeaderName is the name of the lease of the retention duty.
const LeaderName = "retention"

// Leader reports leadership of the duty's lease (leader.Elector).
type Leader interface {
	Term() (int64, bool)
}

// Config configures a Retention.
type Config struct {
	Store     store.TxRunner
	Leader    Leader // holder of the LeaderName lease
	Holder    string // this controller's id
	AuditDays int    // 0 keeps the audit trail forever
	Interval  time.Duration
	Now       func() time.Time
	Logger    *slog.Logger
	// CallTimeout bounds one batch transaction; keep it below the shutdown timeout.
	CallTimeout time.Duration
}

// Retention runs the duty.
type Retention struct {
	cfg    Config
	log    *slog.Logger
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// New validates cfg and returns a stopped Retention.
func New(cfg Config) (*Retention, error) {
	if cfg.Store == nil || cfg.Leader == nil || cfg.Holder == "" {
		return nil, errors.New("retention: a store, a leader and a holder are required")
	}
	if cfg.AuditDays < 0 {
		return nil, errors.New("retention: audit days must not be negative")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Hour
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = 10 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Retention{cfg: cfg, log: log.With("component", "retention")}, nil
}

// Start begins the periodic pass. It does nothing when no policy is set.
func (r *Retention) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return errors.New("retention: already started")
	}
	if r.cfg.AuditDays == 0 {
		return nil
	}
	ctx, r.cancel = context.WithCancel(context.WithoutCancel(ctx))
	r.done = make(chan struct{})
	go r.run(ctx, r.done)
	return nil
}

// Stop ends the loop and waits for it.
func (r *Retention) Stop(ctx context.Context) error {
	r.mu.Lock()
	cancel, done := r.cancel, r.done
	r.cancel = nil
	r.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stopping retention: %w", ctx.Err())
	}
}

func (r *Retention) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	t := time.NewTicker(r.cfg.Interval)
	defer t.Stop()
	r.Once(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Once(ctx)
		}
	}
}

// PrunedDetails is the details of the audit.prune event: a count and the
// cutoff, never any content of the deleted events.
type PrunedDetails struct {
	Deleted       int64     `json:"deleted"`
	Before        time.Time `json:"before"`
	RetentionDays int       `json:"retention_days"`
}

// Once runs one pass: while this controller leads, it deletes the audit events
// older than the retention in batches. Each batch is one transaction fenced by
// the lease term that also writes the batch's audit.prune event.
func (r *Retention) Once(ctx context.Context) {
	if r.cfg.AuditDays == 0 {
		return
	}
	term, ok := r.cfg.Leader.Term()
	if !ok {
		return
	}
	now := r.cfg.Now()
	cutoff := now.AddDate(0, 0, -r.cfg.AuditDays)
	actx := store.WithAudit(ctx, store.AuditMeta{Actor: Actor})
	var total int64
	for i := 0; i < maxBatches && ctx.Err() == nil; i++ {
		var n int64
		cctx, cancel := context.WithTimeout(actx, r.cfg.CallTimeout)
		err := r.cfg.Store.WithTx(cctx, func(tx store.Tx) error {
			if err := tx.CheckLeader(cctx, LeaderName, r.cfg.Holder, term, now); err != nil {
				return err
			}
			var perr error
			n, perr = tx.PruneAuditEvents(cctx, cutoff, batch, now)
			return perr
		})
		cancel()
		if errors.Is(err, store.ErrStale) {
			r.log.Warn("lost the retention lease; skipping the rest of this pass")
			break
		}
		if err != nil {
			if ctx.Err() == nil {
				r.log.Error("pruning audit events", "error", err)
			}
			break
		}
		total += n
		if n < batch {
			break
		}
	}
	if total > 0 {
		r.log.Info("pruned audit events", "deleted", total, "before", cutoff)
	}
}
