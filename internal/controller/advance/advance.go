package advance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/store"
)

// maxConflicts bounds the reload-and-retry loop after a lost version CAS
// within one job attempt; after that the job fails and River retries it.
const maxConflicts = 5

// ErrConflicts means the run kept changing under the advance.
var ErrConflicts = errors.New("advance: run kept changing")

// decider is the part of engine.Engine the advance needs.
type decider interface {
	Parse(data []byte) (*engine.Job, error)
	Decide(job *engine.Job, st engine.RunState, now time.Time) ([]engine.Action, error)
}

// advanceStore is the part of the store the advance needs.
type advanceStore interface {
	store.TxRunner
	GetRunTree(ctx context.Context, id string) (store.RunTree, error)
	GetJob(ctx context.Context, id string) (store.Job, error)
	GetJobVersion(ctx context.Context, id string) (store.JobVersion, error)
	ListRunTargets(ctx context.Context, runID string) ([]store.RunTarget, error)
	ListProjects(ctx context.Context) ([]store.Project, error)
	ListPluginManifests(ctx context.Context) ([]store.PluginManifest, error)
}

// core is the advance logic without River, so it is testable on its own.
type core struct {
	st      advanceStore
	eng     decider
	now     func() time.Time
	log     *slog.Logger
	enqueue func(ctx context.Context, tx store.Tx, runID string) error
	// wake schedules the job-level timeout wake-up (nil: none, in tests).
	wake   func(ctx context.Context, tx store.Tx, runID string, at time.Time) error
	notify func(runID string)
}

// advance runs one load-decide-apply cycle for runID, reloading when the
// version CAS is lost. It writes nothing when the engine has nothing to do,
// which makes it idempotent: a duplicate or late job is harmless.
func (c *core) advance(ctx context.Context, runID string) error {
	for range maxConflicts {
		changed, err := c.once(ctx, runID)
		if errors.Is(err, store.ErrStale) {
			c.log.Debug("run advance lost the version race; reloading", "run_id", runID)
			continue
		}
		if err != nil {
			return err
		}
		if changed {
			c.notify(runID)
		}
		return nil
	}
	return fmt.Errorf("run %s: %w", runID, ErrConflicts)
}

// once is one cycle. It returns changed=true after a committed apply.
func (c *core) once(ctx context.Context, runID string) (changed bool, err error) {
	now := c.now() // one instant per cycle: Decide and the stored timestamps agree
	tree, err := c.st.GetRunTree(ctx, runID)
	if err != nil {
		return false, fmt.Errorf("loading run %s: %w", runID, err)
	}
	if tree.Status.Terminal() {
		return false, nil
	}
	job, meta, err := c.loadJob(ctx, tree)
	if err != nil {
		return false, err
	}
	state, err := buildState(tree, meta)
	if err != nil {
		return false, &permanentError{err}
	}
	if err := c.fillIdempotent(ctx, &state, job); err != nil {
		return false, err
	}
	if engine.UsesTargets(job) {
		if state.Targets, err = c.frozenTargets(ctx, runID); err != nil {
			return false, err // not permanent: River retries
		}
	}
	acts, err := c.eng.Decide(job, state, now)
	if err != nil {
		return false, &permanentError{fmt.Errorf("deciding run %s: %w", runID, err)}
	}
	adv, wakes, err := toAdvance(acts, tree, job)
	if err != nil {
		return false, &permanentError{err}
	}
	if adv.Empty() {
		// A Decide can ask only for a wake (the end of a finally grace while a
		// finally step runs): nothing to apply, but the wake must be queued or
		// nothing runs the advance then.
		if err := c.queueWakes(ctx, runID, wakes); err != nil {
			return false, err
		}
		return false, nil
	}
	err = c.st.WithTx(ctx, func(tx store.Tx) error {
		if _, err := tx.ApplyAdvance(ctx, runID, tree.Version, adv, now); err != nil {
			return err
		}
		// Wake-ups (the job-level timeout's deadline, the end of a finally grace) are queued in the same transaction.
		if c.wake != nil {
			for _, at := range wakes {
				if err := c.wake(ctx, tx, runID, at); err != nil {
					return err
				}
			}
		}
		// A cancelled queued execution finishes its step without any agent
		// report, so nothing else would wake the run: advance again. (For a
		// flagged one the second advance is empty.) The same goes for the
		// job timeout, whose cancel is recorded in this advance, and for a
		// settled step: the Decide that settles it waits, and the next one
		// reads the finished step.
		if len(adv.Cancel) > 0 || adv.CancelRun != "" || len(adv.Settle) > 0 {
			return c.enqueue(ctx, tx, runID)
		}
		return nil
	})
	if errors.Is(err, store.ErrInvalid) { // a change set the store rejects stays rejected
		return false, &permanentError{err}
	}
	if err != nil {
		return false, err
	}
	c.log.Debug("run advanced", "run_id", runID, "version", tree.Version+1, "start", adv.Start, "steps", len(adv.NewSteps), "finish", string(adv.Finish))
	return true, nil
}

func (c *core) loadJob(ctx context.Context, tree store.RunTree) (*engine.Job, jobMeta, error) {
	jv, err := c.st.GetJobVersion(ctx, tree.JobVersionID)
	if err != nil {
		return nil, jobMeta{}, fmt.Errorf("loading job version of run %s: %w", tree.ID, err)
	}
	job, err := c.eng.Parse(jv.Spec)
	if err != nil {
		return nil, jobMeta{}, &permanentError{fmt.Errorf("parsing job version %s of run %s: %w", jv.ID, tree.ID, err)}
	}
	sj, err := c.st.GetJob(ctx, tree.JobID)
	if err != nil {
		return nil, jobMeta{}, fmt.Errorf("loading job of run %s: %w", tree.ID, err)
	}
	projects, err := c.st.ListProjects(ctx)
	if err != nil {
		return nil, jobMeta{}, fmt.Errorf("listing projects: %w", err)
	}
	meta := jobMeta{Name: sj.Name, Version: jv.Version}
	for _, p := range projects {
		if p.ID == sj.ProjectID {
			meta.Project = p.Name
		}
	}
	return job, meta, nil
}

// failRun moves a run the engine cannot advance to failed, so it neither
// lingers as running nor gets re-queued forever by the sweep. Steps that never
// ran stay as they are; the dispatcher only claims executions of runs that are
// not terminal.
func (c *core) failRun(ctx context.Context, runID string) error {
	for range maxConflicts {
		tree, err := c.st.GetRunTree(ctx, runID)
		if err != nil {
			return fmt.Errorf("loading run %s to fail it: %w", runID, err)
		}
		if tree.Status.Terminal() {
			return nil
		}
		err = c.st.WithTx(ctx, func(tx store.Tx) error {
			_, err := tx.ApplyAdvance(ctx, runID, tree.Version, store.Advance{Finish: store.RunFailed}, c.now())
			return err
		})
		if errors.Is(err, store.ErrStale) {
			continue
		}
		if err != nil {
			return err
		}
		c.notify(runID)
		return nil
	}
	return fmt.Errorf("run %s: %w", runID, ErrConflicts)
}

// permanentError marks a failure that retrying cannot fix (a spec the engine
// rejects, corrupt stored JSON). The worker cancels the River job instead of
// retrying it.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// queueWakes inserts the wake jobs of a Decide that changed nothing, in a
// transaction of their own. A wake is unique per (run, deadline), so asking
// again while one is queued adds nothing.
func (c *core) queueWakes(ctx context.Context, runID string, wakes []time.Time) error {
	if len(wakes) == 0 || c.wake == nil {
		return nil
	}
	return c.st.WithTx(ctx, func(tx store.Tx) error {
		for _, at := range wakes {
			if err := c.wake(ctx, tx, runID, at); err != nil {
				return err
			}
		}
		return nil
	})
}

// frozenTargets reads the run's target list as it was when the run started
// (#117); a run without a snapshot has none.
func (c *core) frozenTargets(ctx context.Context, runID string) ([]map[string]any, error) {
	rows, err := c.st.ListRunTargets(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("reading the targets of run %s: %w", runID, err)
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		var m map[string]any
		if err := json.Unmarshal(r.Node, &m); err != nil {
			return nil, &permanentError{fmt.Errorf("target %q of run %s is not an object: %w", r.Name, runID, err)}
		}
		out = append(out, m)
	}
	return out, nil
}
