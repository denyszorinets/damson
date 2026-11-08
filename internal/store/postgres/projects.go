package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres/gen"
)

var _ store.ProjectStore = (*Store)(nil)

func toProject(r gen.Project) (store.Project, error) {
	rules, err := store.DecodeAccessRules([]byte(r.AccessRules))
	if err != nil {
		return store.Project{}, err
	}
	return store.Project{
		ID: r.ID, Path: r.Path, ParentID: r.ParentID.String, Description: r.Description, Version: int(r.Version),
		CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(), AccessRules: rules,
	}, nil
}

func toProjects(rows []gen.Project) ([]store.Project, error) {
	out := make([]store.Project, len(rows))
	for i, r := range rows {
		p, err := toProject(r)
		if err != nil {
			return nil, err
		}
		out[i] = p
	}
	return out, nil
}

// GetProjectByPath implements store.ProjectStore.
func (s *Store) GetProjectByPath(ctx context.Context, path string) (store.Project, error) {
	if store.ValidateProjectPath(path) != nil { // never a project; also keeps NUL bytes away from the database
		return store.Project{}, fmt.Errorf("project %q: %w", "invalid", store.ErrNotFound)
	}
	row, err := s.q.GetProjectByPath(ctx, path)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Project{}, fmt.Errorf("project %q: %w", path, store.ErrNotFound)
	}
	if err != nil {
		return store.Project{}, fmt.Errorf("getting project %q: %w", path, err)
	}
	return toProject(row)
}

// GetProject implements store.ProjectStore.
func (s *Store) GetProject(ctx context.Context, id string) (store.Project, error) {
	row, err := s.q.GetProject(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Project{}, fmt.Errorf("project %q: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.Project{}, fmt.Errorf("getting project %q: %w", id, err)
	}
	return toProject(row)
}

// ListProjects implements store.ProjectStore.
func (s *Store) ListProjects(ctx context.Context) ([]store.Project, error) {
	rows, err := s.q.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	return toProjects(rows)
}

// ListProjectSubtree implements store.ProjectStore.
func (s *Store) ListProjectSubtree(ctx context.Context, path string) ([]store.Project, error) {
	rows, err := s.q.ListProjectSubtree(ctx, gen.ListProjectSubtreeParams{Path: path, PathSlash: path + "/", PathNext: path + "0"})
	if err != nil {
		return nil, fmt.Errorf("listing project subtree: %w", err)
	}
	return toProjects(rows)
}

// CreateProject implements store.ProjectStore.
func (s *Store) CreateProject(ctx context.Context, id, path, description string, now time.Time) (store.Project, error) {
	if id == "" {
		return store.Project{}, fmt.Errorf("a project needs an id: %w", store.ErrInvalid)
	}
	if err := store.ValidateProjectPath(path); err != nil {
		return store.Project{}, err
	}
	ev, err := store.NewProjectEvent(ctx, store.ActionProjectCreate, id, store.ProjectAuditDetails{Path: path, Description: description}, now)
	if err != nil {
		return store.Project{}, err
	}
	var parentID pgtype.Text
	err = s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		if parent := store.ParentPath(path); parent != "" {
			p, err := q.GetProjectByPath(ctx, parent)
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("parent project %q: %w", parent, store.ErrNotFound)
			}
			if err != nil {
				return fmt.Errorf("reading parent project: %w", err)
			}
			parentID = pgtype.Text{String: p.ID, Valid: true}
		}
		err := q.InsertProject(ctx, gen.InsertProjectParams{ID: id, Path: path, ParentID: parentID, Description: description, Now: ts(now)})
		if isUniqueViolation(err) {
			return fmt.Errorf("project %q: %w", path, store.ErrConflict)
		}
		if hasCode(err, foreignKeyViolation) { // the parent was deleted since it was read
			return fmt.Errorf("parent project of %q: %w", path, store.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("creating project: %w", err)
		}
		return tx.AppendAuditEvent(ctx, ev)
	})
	if err != nil {
		return store.Project{}, err
	}
	n := store.NormalizeTime(now)
	return store.Project{ID: id, Path: path, ParentID: parentID.String, Description: description, Version: 1, CreatedAt: n, UpdatedAt: n}, nil
}

// UpdateProject implements store.ProjectStore.
func (s *Store) UpdateProject(ctx context.Context, id, description string, expectedVersion int, now time.Time) (store.Project, error) {
	if _, err := store.AuditFrom(ctx); err != nil {
		return store.Project{}, err
	}
	var out store.Project
	err := s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		cur, err := q.GetProject(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("project %q: %w", id, store.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading project: %w", err)
		}
		n, err := q.UpdateProjectCAS(ctx, gen.UpdateProjectCASParams{
			ID: id, Description: description, UpdatedAt: ts(now), ExpectedVersion: int32(expectedVersion),
		})
		if err != nil {
			return fmt.Errorf("updating project: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("project %q version %d: %w", id, expectedVersion, store.ErrStale)
		}
		ev, err := store.NewProjectEvent(ctx, store.ActionProjectUpdate, id, store.ProjectAuditDetails{Path: cur.Path, Description: description}, now)
		if err != nil {
			return err
		}
		cur.Description, cur.Version, cur.UpdatedAt = description, int32(expectedVersion)+1, ts(now)
		if out, err = toProject(cur); err != nil {
			return err
		}
		return tx.AppendAuditEvent(ctx, ev)
	})
	return out, err
}

// DeleteProject implements store.ProjectStore.
func (s *Store) DeleteProject(ctx context.Context, id string, now time.Time) error {
	if _, err := store.AuditFrom(ctx); err != nil {
		return err
	}
	return s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		cur, err := q.GetProject(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("project %q: %w", id, store.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading project: %w", err)
		}
		counts := []struct {
			what string
			n    func() (int64, error)
		}{
			{"child projects", func() (int64, error) { return q.CountProjectChildren(ctx, pgtype.Text{String: id, Valid: true}) }},
			{"jobs", func() (int64, error) { return q.CountProjectJobs(ctx, id) }},
			{"secrets", func() (int64, error) { return q.CountProjectSecrets(ctx, cur.Path) }},
			{"active tokens", func() (int64, error) { return q.CountProjectTokens(ctx, cur.Path) }},
		}
		for _, c := range counts {
			n, err := c.n()
			if err != nil {
				return fmt.Errorf("counting project %s: %w", c.what, err)
			}
			if n > 0 {
				return &store.ProjectInUseError{Path: cur.Path, What: c.what}
			}
		}
		if n, err := q.DeleteProject(ctx, id); hasCode(err, foreignKeyViolation) { // a child or job committed since the counts
			return &store.ProjectInUseError{Path: cur.Path, What: "child projects or jobs"}
		} else if err != nil {
			return fmt.Errorf("deleting project: %w", err)
		} else if n == 0 {
			return fmt.Errorf("project %q: %w", id, store.ErrNotFound)
		}
		ev, err := store.NewProjectEvent(ctx, store.ActionProjectDelete, id, store.ProjectAuditDetails{Path: cur.Path}, now)
		if err != nil {
			return err
		}
		return tx.AppendAuditEvent(ctx, ev)
	})
}
