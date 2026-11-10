// Package retention is the controller duty that deletes data past its
// retention (ARCHITECTURE 8.4): the audit trail when audit.retention_days is
// set (ADR-0054), finished runs with their logs when runs.retention_days is set
// (ADR-0074), and expired sessions.
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
	RunDays   int    // 0 keeps finished runs forever (ADR-0074)
	// Sessions, if set, has expired sessions and logins deleted on each pass.
	// They were already unusable, so the delete needs no fencing.
	Sessions    store.SessionStore
	SessionIdle time.Duration
	Interval    time.Duration
	Now         func() time.Time
	Logger      *slog.Logger
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
	if cfg.AuditDays < 0 || cfg.RunDays < 0 {
		return nil, errors.New("retention: retention days must not be negative")
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
	if r.cfg.AuditDays == 0 && r.cfg.RunDays == 0 && r.cfg.Sessions == nil {
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
	term, ok := r.cfg.Leader.Term()
	if !ok {
		return
	}
	r.purgeAuth(ctx)
	r.pruneRuns(ctx, term)
	if r.cfg.AuditDays == 0 {
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

// purgeAuth deletes expired sessions and logins.
func (r *Retention) purgeAuth(ctx context.Context) {
	if r.cfg.Sessions == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, r.cfg.CallTimeout)
	defer cancel()
	ns, nl, err := r.cfg.Sessions.PurgeExpiredAuth(cctx, r.cfg.Now(), r.cfg.SessionIdle)
	if err != nil {
		r.log.Warn("purging expired sessions", "error", err)
		return
	}
	if ns+nl > 0 {
		r.log.Info("purged expired sessions and logins", "sessions", ns, "logins", nl)
	}
}

// runBatch is how many runs one transaction deletes at most: each run takes
// its steps, executions and log chunks with it. A batch that times out is
// retried with half as many runs, down to one.
const runBatch = 20

// maxRunBatches is a safety cap on batches in one pass; the time budget
// (half the interval) normally ends the pass first.
const maxRunBatches = 100000

// pruneRuns deletes finished runs older than the retention, in fenced batches,
// for at most half an interval. Log objects in external backends are queued for
// the log sweeper in the same transaction (ADR-0067).
func (r *Retention) pruneRuns(ctx context.Context, term int64) {
	if r.cfg.RunDays == 0 {
		return
	}
	cutoff := r.cfg.Now().AddDate(0, 0, -r.cfg.RunDays) // fixed for the pass
	budget := time.Now().Add(r.cfg.Interval / 2)
	actx := store.WithAudit(ctx, store.AuditMeta{Actor: Actor})
	size := runBatch
	var total store.RunPrune
	for i := 0; i < maxRunBatches && ctx.Err() == nil && time.Now().Before(budget); i++ {
		var got store.RunPrune
		now := r.cfg.Now() // per batch: the lease check and the event use the current time
		cctx, cancel := context.WithTimeout(actx, r.cfg.CallTimeout)
		err := r.cfg.Store.WithTx(cctx, func(tx store.Tx) error {
			if err := tx.CheckLeader(cctx, LeaderName, r.cfg.Holder, term, now); err != nil {
				return err
			}
			var perr error
			got, perr = tx.PruneRuns(cctx, cutoff, size, now)
			return perr
		})
		timedOut := errors.Is(cctx.Err(), context.DeadlineExceeded)
		cancel()
		if errors.Is(err, store.ErrStale) {
			r.log.Warn("lost the retention lease or a run changed; skipping the rest of this pass")
			break
		}
		if err != nil && timedOut && size > 1 {
			size = max(1, size/2)
			r.log.Warn("pruning runs timed out; retrying with a smaller batch", "batch", size)
			continue
		}
		if err != nil {
			if ctx.Err() == nil {
				r.log.Error("pruning runs", "error", err)
			}
			break
		}
		total.Runs += got.Runs
		total.Executions += got.Executions
		total.Chunks += got.Chunks
		total.Objects += got.Objects
		total.Queued += got.Queued
		if got.Runs < int64(size) {
			break
		}
	}
	if total.Runs > 0 {
		r.log.Info("pruned finished runs", "runs", total.Runs, "executions", total.Executions, "log_chunks", total.Chunks, "queued_log_deletes", total.Queued, "before", cutoff)
	}
}
