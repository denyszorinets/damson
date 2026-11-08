package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres/gen"
)

var _ store.AccessStore = (*Store)(nil)

// ListProjectChain implements store.AccessStore.
func (s *Store) ListProjectChain(ctx context.Context, path string) ([]store.Project, error) {
	if store.ValidateProjectPath(path) != nil {
		return nil, nil
	}
	rows, err := s.q.ListProjectChain(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("listing project chain: %w", err)
	}
	return toProjects(rows)
}

// SetProjectAccessRules implements store.AccessStore.
func (s *Store) SetProjectAccessRules(ctx context.Context, id string, rules []store.AccessRule, expectedVersion int, now time.Time) (store.Project, error) {
	if _, err := store.AuditFrom(ctx); err != nil {
		return store.Project{}, err
	}
	raw, err := store.EncodeAccessRules(rules, store.RoleAdmin)
	if err != nil {
		return store.Project{}, err
	}
	var out store.Project
	err = s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		cur, err := q.GetProject(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("project %q: %w", id, store.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading project: %w", err)
		}
		n, err := q.SetProjectAccessRulesCAS(ctx, gen.SetProjectAccessRulesCASParams{
			ID: id, AccessRules: string(raw), UpdatedAt: ts(now), ExpectedVersion: int32(expectedVersion),
		})
		if err != nil {
			return fmt.Errorf("setting access rules: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("project %q version %d: %w", id, expectedVersion, store.ErrStale)
		}
		ev, err := store.NewAccessEvent(ctx, store.ActionProjectAccessRulesUpdate, store.AuditTargetProject, id, cur.Path, rules, "", now)
		if err != nil {
			return err
		}
		cur.AccessRules, cur.Version, cur.UpdatedAt = string(raw), int32(expectedVersion)+1, ts(now)
		if out, err = toProject(cur); err != nil {
			return err
		}
		return tx.AppendAuditEvent(ctx, ev)
	})
	return out, err
}

// GetJobAccess implements store.AccessStore.
func (s *Store) GetJobAccess(ctx context.Context, jobID string) (store.JobAccess, error) {
	r, err := s.q.GetJobAccess(ctx, jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.JobAccess{JobID: jobID}, nil
	}
	if err != nil {
		return store.JobAccess{}, fmt.Errorf("reading job access rules: %w", err)
	}
	rules, err := store.DecodeAccessRules([]byte(r.Rules))
	return store.JobAccess{JobID: jobID, Rules: rules, Version: int(r.Version)}, err
}

// SetJobAccessRules implements store.AccessStore.
func (s *Store) SetJobAccessRules(ctx context.Context, jobID string, rules []store.AccessRule, expectedVersion int, now time.Time) (store.JobAccess, error) {
	if _, err := store.AuditFrom(ctx); err != nil {
		return store.JobAccess{}, err
	}
	raw, err := store.EncodeAccessRules(rules, store.RoleEditor)
	if err != nil {
		return store.JobAccess{}, err
	}
	err = s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		job, err := q.GetJob(ctx, jobID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("job %q: %w", jobID, store.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading job: %w", err)
		}
		proj, err := q.GetProject(ctx, job.ProjectID)
		if err != nil {
			return fmt.Errorf("reading project of job: %w", err)
		}
		if expectedVersion == 0 {
			err = q.InsertJobAccess(ctx, gen.InsertJobAccessParams{JobID: jobID, Rules: string(raw), UpdatedAt: ts(now)})
			if isUniqueViolation(err) {
				return fmt.Errorf("job %q access rules: %w", jobID, store.ErrStale)
			}
			if err != nil {
				return fmt.Errorf("creating access rules: %w", err)
			}
		} else {
			n, err := q.UpdateJobAccessCAS(ctx, gen.UpdateJobAccessCASParams{
				JobID: jobID, Rules: string(raw), UpdatedAt: ts(now), ExpectedVersion: int32(expectedVersion),
			})
			if err != nil {
				return fmt.Errorf("updating access rules: %w", err)
			}
			if n == 0 {
				return fmt.Errorf("job %q access rules version %d: %w", jobID, expectedVersion, store.ErrStale)
			}
		}
		ev, err := store.NewAccessEvent(ctx, store.ActionJobAccessRulesUpdate, store.AuditTargetJob, jobID, proj.Path, rules, jobID, now)
		if err != nil {
			return err
		}
		return tx.AppendAuditEvent(ctx, ev)
	})
	if err != nil {
		return store.JobAccess{}, err
	}
	return store.JobAccess{JobID: jobID, Rules: rules, Version: expectedVersion + 1}, nil
}
