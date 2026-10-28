package advance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/riverdriver/riversqlite"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/store"
)

// Config configures an Advancer.
type Config struct {
	// Store is the database. It must be a Postgres or SQLite backend that
	// exposes its pool (Pool() or QueueDB()) and must have been migrated
	// (Store.Migrate applies River's schema too).
	Store store.Store
	// Engine decides what a run does next.
	Engine *engine.Engine
	// Workers bounds concurrent advances on this controller; default 4,
	// and 1 on SQLite, which has a single writer.
	Workers int
	// PollInterval is how often River looks for new jobs; default 1s.
	PollInterval time.Duration
	// SweepInterval is how often the safety-net sweep re-enqueues every
	// pending or running run; default 1m, negative disables it.
	SweepInterval time.Duration
	// Now is the clock handed to the engine; default time.Now.
	Now func() time.Time
	// Hub receives a signal after each committed advance. A new Hub is made
	// when nil; read it back with Advancer.Hub.
	Hub    *Hub
	Logger *slog.Logger
}

// DefaultSweepInterval is the safety-net sweep period.
const DefaultSweepInterval = time.Minute

// queue is the dialect-specific River client behind an Advancer.
type queue interface {
	start(ctx context.Context) error
	stop(ctx context.Context) error
	insert(ctx context.Context, tx store.Tx, a Args) error
}

// Advancer owns the River client that runs run.advance jobs. Start launches
// the workers, Stop drains them. Enqueue works without Start (insert only).
type Advancer struct {
	core    *core
	q       queue
	hub     *Hub
	log     *slog.Logger
	st      store.Store
	sweep   time.Duration
	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// New builds an Advancer. It does not touch the network.
func New(cfg Config) (*Advancer, error) {
	if cfg.Store == nil || cfg.Engine == nil {
		return nil, errors.New("advance: store and engine are required")
	}
	a := &Advancer{hub: cfg.Hub, log: cfg.Logger, st: cfg.Store, sweep: cfg.SweepInterval}
	if a.sweep == 0 {
		a.sweep = DefaultSweepInterval
	}
	if a.hub == nil {
		a.hub = NewHub()
	}
	if a.log == nil {
		a.log = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	a.core = &core{st: cfg.Store, eng: cfg.Engine, now: now, log: a.log, enqueue: a.Enqueue, notify: a.hub.Notify}
	q, err := newQueue(cfg, a.core, a.log)
	if err != nil {
		return nil, err
	}
	a.q = q
	return a, nil
}

// Hub returns the in-process change notifier (wire it to the API's run event
// streams).
func (a *Advancer) Hub() *Hub { return a.hub }

// Start launches the River workers. It returns once they are running.
func (a *Advancer) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return errors.New("advance: already started")
	}
	if err := a.q.start(ctx); err != nil {
		return fmt.Errorf("starting run.advance workers: %w", err)
	}
	a.started = true
	if a.sweep > 0 {
		sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		a.cancel, a.done = cancel, make(chan struct{})
		go a.sweepLoop(sctx, a.done)
	}
	return nil
}

// Stop stops fetching jobs and waits for running ones until ctx ends; then it
// cancels them (a cancelled job is retried by River on the next controller).
func (a *Advancer) Stop(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.started {
		return nil
	}
	a.started = false
	if a.cancel != nil {
		a.cancel()
		<-a.done
		a.cancel = nil
	}
	if err := a.q.stop(ctx); err != nil {
		return fmt.Errorf("stopping run.advance workers: %w", err)
	}
	return nil
}

// Enqueue schedules a run.advance for runID inside tx: the job exists if and
// only if the caller's transaction commits. Call it in the same transaction
// as the change that makes the run advanceable (insert, cancel request,
// execution completion).
//
// The job is unique per (run, version); see Args.InsertOpts. A caller whose
// own writes do not bump runs.version must call tx.TouchRun first.
func (a *Advancer) Enqueue(ctx context.Context, tx store.Tx, runID string) error {
	run, err := tx.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("reading run %s to enqueue: %w", runID, err)
	}
	if err := a.q.insert(ctx, tx, Args{RunID: runID, Version: run.Version}); err != nil {
		return fmt.Errorf("enqueueing run.advance for %s: %w", runID, err)
	}
	return nil
}

// Advance runs one load-decide-apply cycle synchronously. The River worker
// calls it; tests and tools may too.
func (a *Advancer) Advance(ctx context.Context, runID string) error {
	return a.core.advance(ctx, runID)
}

// worker is the River worker of run.advance.
type worker struct {
	river.WorkerDefaults[Args]
	core *core
}

// Work implements river.Worker. Failures that retrying cannot fix cancel the
// job; everything else is retried by River with backoff.
func (w *worker) Work(ctx context.Context, job *river.Job[Args]) error {
	err := w.core.advance(ctx, job.Args.RunID)
	var perm *permanentError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &perm), errors.Is(err, store.ErrNotFound):
		w.core.log.Error("run advance cannot succeed; dropping job", "run_id", job.Args.RunID, "error", err)
		return river.JobCancel(err)
	}
	return err
}

func newQueue(cfg Config, c *core, log *slog.Logger) (queue, error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &worker{core: c})
	rc := func(n int) *river.Config {
		poll := cfg.PollInterval
		if poll <= 0 {
			poll = time.Second
		}
		return &river.Config{
			Logger:            log,
			Workers:           workers,
			Queues:            map[string]river.QueueConfig{QueueName: {MaxWorkers: n}},
			FetchPollInterval: poll,
			FetchCooldown:     min(poll, 100*time.Millisecond),
		}
	}
	switch s := cfg.Store.(type) {
	case interface{ Pool() *pgxpool.Pool }:
		cl, err := river.NewClient(riverpgxv5.New(s.Pool()), rc(workersOr(cfg.Workers, 4)))
		if err != nil {
			return nil, fmt.Errorf("creating river postgres client: %w", err)
		}
		return &riverQueue[pgx.Tx]{client: cl, unwrap: func(n any) (pgx.Tx, bool) { tx, ok := n.(pgx.Tx); return tx, ok }}, nil
	case interface{ QueueDB() *sql.DB }:
		cl, err := river.NewClient(riversqlite.New(s.QueueDB()), rc(workersOr(cfg.Workers, 1)))
		if err != nil {
			return nil, fmt.Errorf("creating river sqlite client: %w", err)
		}
		return &riverQueue[*sql.Tx]{client: cl, unwrap: func(n any) (*sql.Tx, bool) { tx, ok := n.(*sql.Tx); return tx, ok }}, nil
	}
	return nil, fmt.Errorf("advance: store %T exposes no Postgres pool or SQLite queue pool", cfg.Store)
}

func workersOr(n, def int) int {
	if n > 0 {
		return n
	}
	return def
}

// riverQueue adapts a River client of one transaction type to queue.
type riverQueue[T any] struct {
	client *river.Client[T]
	unwrap func(native any) (T, bool)
}

func (r *riverQueue[T]) start(ctx context.Context) error { return r.client.Start(ctx) }

func (r *riverQueue[T]) stop(ctx context.Context) error {
	if err := r.client.Stop(ctx); err != nil {
		if ctx.Err() != nil {
			// The graceful stop ran out of time: cancel the jobs and wait.
			return r.client.StopAndCancel(context.WithoutCancel(ctx))
		}
		return err
	}
	return nil
}

func (r *riverQueue[T]) insert(ctx context.Context, tx store.Tx, a Args) error {
	native, ok := r.unwrap(tx.Native())
	if !ok {
		return fmt.Errorf("transaction %T does not belong to this queue's database", tx.Native())
	}
	_, err := r.client.InsertTx(ctx, native, a, nil)
	return err
}
