package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite/gen"
)

var _ store.ApplyStore = (*Store)(nil)

// ApplyState implements store.ApplyStore.
func (s *Store) ApplyState(ctx context.Context, source string, projectPaths []string) (store.ApplyState, error) {
	var st store.ApplyState
	rows, err := s.q.ListProjects(ctx)
	if err != nil {
		return st, fmt.Errorf("listing projects: %w", err)
	}
	if st.Projects, err = toProjects(rows); err != nil {
		return st, err
	}
	seen := map[string]bool{}
	var addErr error
	add := func(id, projectID, path, name, managedBy string, version int, archived sql.NullString, checksum string, bindings int) {
		if seen[id] || addErr != nil {
			return
		}
		seen[id] = true
		at, err := archivedPtr(archived)
		if err != nil {
			addErr = err
			return
		}
		st.Jobs = append(st.Jobs, store.ApplyJob{
			Job:         store.Job{ID: id, ProjectID: projectID, Name: name, ManagedBy: managedBy, CurrentVersion: version, ArchivedAt: at},
			ProjectPath: path, Checksum: checksum, Bindings: bindings,
		})
	}
	want := make(map[string]bool, len(projectPaths))
	for _, pp := range projectPaths {
		want[pp] = true
	}
	for _, p := range st.Projects {
		if !want[p.Path] {
			continue
		}
		jobs, err := s.q.ListApplyJobsByProject(ctx, p.ID)
		if err != nil {
			return st, fmt.Errorf("listing jobs of a project: %w", err)
		}
		for _, j := range jobs {
			add(j.ID, j.ProjectID, j.ProjectPath, j.Name, j.ManagedBy, int(j.CurrentVersion), j.ArchivedAt, j.Checksum, int(j.Bindings))
		}
	}
	jobs, err := s.q.ListApplyJobsBySource(ctx, source)
	if err != nil {
		return st, fmt.Errorf("listing jobs of a source: %w", err)
	}
	for _, j := range jobs {
		add(j.ID, j.ProjectID, j.ProjectPath, j.Name, j.ManagedBy, int(j.CurrentVersion), j.ArchivedAt, j.Checksum, int(j.Bindings))
	}
	return st, addErr
}

// ApplyPlan implements store.ApplyStore.
func (s *Store) ApplyPlan(ctx context.Context, ops []store.ApplyOp, now time.Time) error {
	if _, err := store.AuditFrom(ctx); err != nil {
		return err
	}
	if err := store.ValidateApplyOps(ops); err != nil {
		return err
	}
	return s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		for i, op := range ops {
			if err := s.applyOp(ctx, tx, q, op, now); err != nil {
				return fmt.Errorf("apply op %d (%s): %w", i, op.Kind, err)
			}
		}
		return nil
	})
}

func archivedPtr(v sql.NullString) (*time.Time, error) {
	if !v.Valid {
		return nil, nil
	}
	t, err := parseTime(v.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) applyOp(ctx context.Context, tx store.Tx, q *gen.Queries, op store.ApplyOp, now time.Time) error {
	switch op.Kind {
	case store.OpProjectCreate:
		return applyProjectCreate(ctx, tx, q, op, now)
	case store.OpProjectUpdate:
		return applyProjectUpdate(ctx, tx, q, op, now)
	case store.OpJobCreate:
		return applyJobCreate(ctx, tx, q, op, now)
	case store.OpJobUpdate:
		return applyJobUpdate(ctx, tx, q, op, now)
	default:
		return applyJobArchive(ctx, tx, q, op, now)
	}
}

func audit(ctx context.Context, tx store.Tx, action, targetType, target, project string, d any, now time.Time) error {
	ev, err := store.NewEvent(ctx, action, targetType, target, project, d, now)
	if err != nil {
		return err
	}
	return tx.AppendAuditEvent(ctx, ev)
}

func applyProjectCreate(ctx context.Context, tx store.Tx, q *gen.Queries, op store.ApplyOp, now time.Time) error {
	var parent sql.NullString
	if pp := store.ParentPath(op.Path); pp != "" {
		p, err := q.GetProjectByPath(ctx, pp)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("parent project %q: %w", pp, store.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading parent project: %w", err)
		}
		parent = sql.NullString{String: p.ID, Valid: true}
	}
	raw, err := store.EncodeAccessRules(op.AccessRules, store.RoleAdmin)
	if err != nil {
		return err
	}
	id, err := store.NewID()
	if err != nil {
		return err
	}
	err = q.InsertManagedProject(ctx, gen.InsertManagedProjectParams{
		ID: id, Path: op.Path, ParentID: parent, Description: op.Description, Now: fmtTime(now),
		AccessRules: string(raw), NodeScope: op.NodeScope, ManagedBy: op.Source,
	})
	if isUnique(err) {
		return fmt.Errorf("project %q: %w", op.Path, store.ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("creating project %q: %w", op.Path, err)
	}
	if err := audit(ctx, tx, store.ActionProjectCreate, store.AuditTargetProject, id, op.Path,
		store.ProjectAuditDetails{Path: op.Path, Description: op.Description, Source: op.Source}, now); err != nil {
		return err
	}
	return projectDetailEvents(ctx, tx, id, op.Path, op, len(op.AccessRules) > 0, op.NodeScope != "", now)
}

// projectDetailEvents audits the rules and the scope a project was given.
func projectDetailEvents(ctx context.Context, tx store.Tx, id, path string, op store.ApplyOp, rules, scope bool, now time.Time) error {
	if rules {
		ev, err := store.NewAccessEvent(ctx, store.ActionProjectAccessRulesUpdate, store.AuditTargetProject, id, path, op.AccessRules, "", now)
		if err != nil {
			return err
		}
		if err := tx.AppendAuditEvent(ctx, ev); err != nil {
			return err
		}
	}
	if scope {
		return audit(ctx, tx, store.ActionProjectNodeScopeUpdate, store.AuditTargetProject, id, path, struct {
			Path string `json:"path"`
			Set  bool   `json:"set"`
		}{path, op.NodeScope != ""}, now)
	}
	return nil
}

func applyProjectUpdate(ctx context.Context, tx store.Tx, q *gen.Queries, op store.ApplyOp, now time.Time) error {
	cur, err := q.GetProject(ctx, op.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("project %q: %w", op.ID, store.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("reading project: %w", err)
	}
	raw, err := store.EncodeAccessRules(op.AccessRules, store.RoleAdmin)
	if err != nil {
		return err
	}
	n, err := q.ApplyProjectUpdateCAS(ctx, gen.ApplyProjectUpdateCASParams{
		ID: op.ID, Description: op.Description, AccessRules: string(raw), NodeScope: op.NodeScope, NewManagedBy: op.Source,
		UpdatedAt: fmtTime(now), ExpectedVersion: int64(op.ExpectVersion), ExpectedManagedBy: op.ExpectManaged,
	})
	if err != nil {
		return fmt.Errorf("updating project: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("project %q changed since the plan: %w", cur.Path, store.ErrStale)
	}
	if op.ChangedDescription {
		if err := audit(ctx, tx, store.ActionProjectUpdate, store.AuditTargetProject, op.ID, cur.Path,
			store.ProjectAuditDetails{Path: cur.Path, Description: op.Description, Source: op.Source}, now); err != nil {
			return err
		}
	}
	if err := projectDetailEvents(ctx, tx, op.ID, cur.Path, op, op.ChangedRules, op.ChangedScope, now); err != nil {
		return err
	}
	if op.ExpectManaged != op.Source {
		return audit(ctx, tx, store.ActionProjectManage, store.AuditTargetProject, op.ID, cur.Path,
			store.ManageDetails{Path: cur.Path, From: op.ExpectManaged, To: op.Source}, now)
	}
	return nil
}

func applyJobCreate(ctx context.Context, tx store.Tx, q *gen.Queries, op store.ApplyOp, now time.Time) error {
	proj, err := q.GetProjectByPath(ctx, op.ProjectPath)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("project %q: %w", op.ProjectPath, store.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("reading project: %w", err)
	}
	jobID, err := store.NewID()
	if err != nil {
		return err
	}
	verID, err := store.NewID()
	if err != nil {
		return err
	}
	if _, err := q.InsertJob(ctx, gen.InsertJobParams{
		ID: jobID, ProjectID: proj.ID, Name: op.Name, ManagedBy: op.Source, CurrentVersion: 1, CreatedAt: fmtTime(now), UpdatedAt: fmtTime(now),
	}); isUnique(err) {
		return fmt.Errorf("job %q: %w", op.Name, store.ErrConflict)
	} else if err != nil {
		return fmt.Errorf("creating job %q: %w", op.Name, err)
	}
	if _, err := q.InsertJobVersion(ctx, gen.InsertJobVersionParams{
		ID: verID, JobID: jobID, Version: 1, Spec: string(op.Spec), Checksum: op.Checksum, CreatedAt: fmtTime(now),
	}); err != nil {
		return fmt.Errorf("creating version 1 of job %q: %w", op.Name, err)
	}
	return audit(ctx, tx, store.ActionJobCreate, store.AuditTargetJob, jobID, op.ProjectPath,
		store.JobAuditDetails{Name: op.Name, Version: 1, Checksum: op.Checksum, Source: op.Source}, now)
}

func applyJobUpdate(ctx context.Context, tx store.Tx, q *gen.Queries, op store.ApplyOp, now time.Time) error {
	cur, err := getJob(ctx, q, op.ID)
	if err != nil {
		return err
	}
	proj, err := q.GetProject(ctx, cur.ProjectID)
	if err != nil {
		return fmt.Errorf("reading project of job: %w", err)
	}
	next := op.ExpectCurrent
	if op.Spec != nil {
		next++
	}
	var n int64
	if op.Restore {
		n, err = q.ApplyJobRestoreCAS(ctx, gen.ApplyJobRestoreCASParams{
			ID: op.ID, NewVersion: int64(next), NewManagedBy: op.Source, UpdatedAt: fmtTime(now),
			ExpectedVersion: int64(op.ExpectCurrent), ExpectedManagedBy: op.ExpectManaged,
		})
	} else {
		n, err = q.ApplyJobUpdateCAS(ctx, gen.ApplyJobUpdateCASParams{
			ID: op.ID, NewVersion: int64(next), NewManagedBy: op.Source, UpdatedAt: fmtTime(now),
			ExpectedVersion: int64(op.ExpectCurrent), ExpectedManagedBy: op.ExpectManaged,
		})
	}
	if err != nil {
		return fmt.Errorf("updating job %q: %w", cur.Name, err)
	}
	if n == 0 {
		return fmt.Errorf("job %q changed since the plan: %w", cur.Name, store.ErrStale)
	}
	if op.Spec != nil {
		verID, err := store.NewID()
		if err != nil {
			return err
		}
		if _, err := q.InsertJobVersion(ctx, gen.InsertJobVersionParams{
			ID: verID, JobID: op.ID, Version: int64(next), Spec: string(op.Spec), Checksum: op.Checksum, CreatedAt: fmtTime(now),
		}); err != nil {
			return fmt.Errorf("creating version %d of job %q: %w", next, cur.Name, err)
		}
		if err := audit(ctx, tx, store.ActionJobVersionCreate, store.AuditTargetJob, op.ID, proj.Path,
			store.JobAuditDetails{Name: cur.Name, Version: next, Checksum: op.Checksum, Source: op.Source}, now); err != nil {
			return err
		}
	}
	if op.Restore {
		if err := audit(ctx, tx, store.ActionJobRestore, store.AuditTargetJob, op.ID, proj.Path,
			store.ManageDetails{Name: cur.Name, From: "archived", To: op.Source}, now); err != nil {
			return err
		}
	}
	if op.ExpectManaged != op.Source {
		return audit(ctx, tx, store.ActionJobManage, store.AuditTargetJob, op.ID, proj.Path,
			store.ManageDetails{Name: cur.Name, From: op.ExpectManaged, To: op.Source}, now)
	}
	return nil
}

func applyJobArchive(ctx context.Context, tx store.Tx, q *gen.Queries, op store.ApplyOp, now time.Time) error {
	cur, err := getJob(ctx, q, op.ID)
	if err != nil {
		return err
	}
	proj, err := q.GetProject(ctx, cur.ProjectID)
	if err != nil {
		return fmt.Errorf("reading project of job: %w", err)
	}
	n, err := q.ApplyJobArchiveCAS(ctx, gen.ApplyJobArchiveCASParams{
		ID: op.ID, ArchivedAt: sql.NullString{String: fmtTime(now), Valid: true}, ExpectedVersion: int64(op.ExpectCurrent), ExpectedManagedBy: op.ExpectManaged,
	})
	if err != nil {
		return fmt.Errorf("archiving job %q: %w", cur.Name, err)
	}
	if n == 0 {
		return fmt.Errorf("job %q changed since the plan: %w", cur.Name, store.ErrStale)
	}
	return audit(ctx, tx, store.ActionJobArchive, store.AuditTargetJob, op.ID, proj.Path,
		store.ManageDetails{Name: cur.Name, From: op.ExpectManaged, To: "archived"}, now)
}
