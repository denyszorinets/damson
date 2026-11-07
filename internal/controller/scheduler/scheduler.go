// Package scheduler is the controller duty that starts runs from cron
// schedules (ARCHITECTURE 12.3, ADR-0058).
//
// It is a singleton (ARCHITECTURE 8.4): only the holder of the "scheduler"
// lease fires schedules, and each fire is fenced by the lease term. A fire is
// one transaction that checks the lease, creates the run and moves the
// schedule's next_fire_at with a compare-and-swap on the value it read, so a
// fire time starts at most one run even if two controllers briefly both think
// they lead.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/schedule"
	"github.com/denyszorinets/damson/internal/store"
)

// LeaderName is the name of the lease of the scheduler duty.
const LeaderName = "scheduler"

// Actor is the audit actor of the duty.
const Actor = "system:scheduler"

// Defaults.
const (
	DefaultInterval = 10 * time.Second
	// DefaultMisfireGrace is how late a fire time may be and still count as on
	// time. Later than that, the schedule's misfire policy decides.
	DefaultMisfireGrace = time.Minute
	dueBatch            = 100
	maxPassSchedules    = 1000
	// retryAfter is when a schedule that cannot compute a next fire time looks again.
	retryAfter = time.Hour
)

// Leader reports leadership of the duty's lease (leader.Elector).
type Leader interface {
	Term() (int64, bool)
}

// Starter builds and inserts runs. api.Starter implements it.
type Starter interface {
	Prepare(ctx context.Context, jobID string, options map[string]any, trig api.Trigger) (api.Prepared, error)
	Create(ctx context.Context, tx store.Tx, p api.Prepared) (string, error)
}

// Store is what the duty needs of the store.
type Store interface {
	store.TxRunner
	ListDueSchedules(ctx context.Context, now time.Time, limit int) ([]store.Schedule, error)
}

// Config configures a Scheduler.
type Config struct {
	Store   Store
	Starter Starter
	Leader  Leader
	Holder  string // this controller's id
	// Interval is the tick; default 10s.
	Interval time.Duration
	// MisfireGrace is the lateness tolerated before the misfire policy applies; default 1m.
	MisfireGrace time.Duration
	Now          func() time.Time
	Logger       *slog.Logger
	// CallTimeout bounds the transaction of one fire; keep it below the shutdown timeout.
	CallTimeout time.Duration
}

// Scheduler runs the duty.
type Scheduler struct {
	cfg    Config
	log    *slog.Logger
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// New validates cfg and returns a stopped Scheduler.
func New(cfg Config) (*Scheduler, error) {
	if cfg.Store == nil || cfg.Starter == nil || cfg.Leader == nil || cfg.Holder == "" {
		return nil, errors.New("scheduler: a store, a starter, a leader and a holder are required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.MisfireGrace <= 0 {
		cfg.MisfireGrace = DefaultMisfireGrace
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
	return &Scheduler{cfg: cfg, log: log.With("component", "scheduler")}, nil
}

// Start begins the periodic pass.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return errors.New("scheduler: already started")
	}
	ctx, s.cancel = context.WithCancel(context.WithoutCancel(ctx))
	s.done = make(chan struct{})
	go s.run(ctx, s.done)
	return nil
}

// Stop ends the loop and waits for it.
func (s *Scheduler) Stop(ctx context.Context) error {
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
		return fmt.Errorf("stopping scheduler: %w", ctx.Err())
	}
}

func (s *Scheduler) run(ctx context.Context, done chan struct{}) {
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

// Once runs one pass: while this controller leads, it fires every due schedule.
func (s *Scheduler) Once(ctx context.Context) {
	term, ok := s.cfg.Leader.Term()
	if !ok {
		return
	}
	handled := 0
	for handled < maxPassSchedules && ctx.Err() == nil {
		now := s.cfg.Now()
		due, err := s.cfg.Store.ListDueSchedules(ctx, now, dueBatch)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Error("listing due schedules", "error", err)
			}
			return
		}
		progressed := 0
		for _, sc := range due {
			switch s.fire(ctx, sc, term, now) {
			case fired:
				progressed++
			case lostLease:
				return
			}
		}
		handled += len(due)
		if len(due) < dueBatch || progressed == 0 {
			return
		}
	}
}

type outcome int

const (
	fired outcome = iota
	skipped
	lostLease
)

// fire handles one due schedule. It never returns an error: a failure is
// recorded on the schedule, and a transient one leaves it due for the next tick.
func (s *Scheduler) fire(ctx context.Context, sc store.Schedule, term int64, now time.Time) outcome {
	log := s.log.With("schedule_id", sc.ID, "job_id", sc.JobID)
	if sc.NextFireAt == nil {
		return skipped
	}
	due := *sc.NextFireAt
	f := store.ScheduleFire{ID: sc.ID, Expected: due, ExpectedVersion: sc.Version}
	var nextWarn string
	f.Next, nextWarn = s.next(sc, now)

	var prepared api.Prepared
	switch {
	case sc.Misfire == store.MisfireSkip && now.Sub(due) > s.cfg.MisfireGrace:
		f.Error = "skipped a missed fire time (misfire: skip)"
	default:
		var opts map[string]any
		if err := json.Unmarshal(sc.Options, &opts); err != nil {
			f.Error = "the stored options are not a JSON object"
			break
		}
		trig := api.Trigger{Type: api.TriggerSchedule, ScheduleID: sc.ID, FireAt: due.UTC().Format(time.RFC3339)}
		p, err := s.cfg.Starter.Prepare(ctx, sc.JobID, opts, trig)
		var se *api.StartError
		switch {
		case errors.As(err, &se):
			f.Error = se.Msg
		case err != nil:
			if ctx.Err() == nil {
				log.Error("preparing a scheduled run; it stays due", "error", err)
			}
			return skipped
		default:
			prepared = p
		}
	}

	cctx, cancel := context.WithTimeout(store.WithAudit(ctx, store.AuditMeta{Actor: Actor}), s.cfg.CallTimeout)
	defer cancel()
	err := s.cfg.Store.WithTx(cctx, func(tx store.Tx) error {
		if err := tx.CheckLeader(cctx, LeaderName, s.cfg.Holder, term, s.cfg.Now()); errors.Is(err, store.ErrStale) {
			return errLeaseLost
		} else if err != nil {
			return err
		}
		if f.Error == "" {
			id, err := s.cfg.Starter.Create(cctx, tx, prepared)
			if err != nil {
				return fmt.Errorf("creating the run: %w", err)
			}
			f.RunID, f.Fired = id, now
		}
		if f.Error == "" {
			f.Error = nextWarn
		}
		return tx.FireSchedule(cctx, f)
	})
	switch {
	case err == nil:
		if f.RunID != "" {
			log.Info("scheduled run created", "run_id", f.RunID, "fire_at", due)
		} else {
			log.Warn("scheduled fire made no run", "reason", f.Error, "fire_at", due)
		}
		return fired
	case errors.Is(err, errLeaseLost):
		log.Warn("lost the scheduler lease; ending this pass")
		return lostLease
	case errors.Is(err, store.ErrStale):
		return skipped // another controller fired it, or it was changed
	default:
		if ctx.Err() == nil {
			log.Error("firing a schedule; it stays due", "error", err)
		}
		return skipped
	}
}

var errLeaseLost = errors.New("the scheduler lease is no longer held")

// next computes the schedule's next fire time after now. If the expression can
// no longer be evaluated, it returns why and looks again in an hour.
func (s *Scheduler) next(sc store.Schedule, now time.Time) (time.Time, string) {
	spec, err := schedule.Parse(sc.Cron, sc.Timezone)
	if err != nil {
		return now.Add(retryAfter), "the schedule is invalid: " + err.Error()
	}
	n, ok := spec.Next(now)
	if !ok {
		return now.Add(retryAfter), "the schedule never fires again"
	}
	return n, ""
}
