package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres/gen"
)

var _ store.PoolStore = (*Store)(nil)

// foreignKeyViolation is the SQLSTATE for foreign_key_violation.
const foreignKeyViolation = "23503"

func hasCode(err error, code string) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == code
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t.UTC(), Valid: true} }

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

func toPool(r gen.AgentPool) store.Pool {
	return store.Pool{ID: r.ID, Name: r.Name, Description: r.Description, CreatedAt: r.CreatedAt.Time.UTC()}
}

// CreatePool implements store.PoolStore.
func (s *Store) CreatePool(ctx context.Context, name, description string, now time.Time) (store.Pool, error) {
	id, err := store.NewID()
	if err != nil {
		return store.Pool{}, err
	}
	_, err = s.q.InsertPool(ctx, gen.InsertPoolParams{ID: id, Name: name, Description: description, CreatedAt: ts(now)})
	if hasCode(err, uniqueViolation) {
		return store.Pool{}, fmt.Errorf("pool %q: %w", name, store.ErrConflict)
	}
	if err != nil {
		return store.Pool{}, fmt.Errorf("creating pool %q: %w", name, err)
	}
	return store.Pool{ID: id, Name: name, Description: description, CreatedAt: now.UTC()}, nil
}

// GetPoolByName implements store.PoolStore.
func (s *Store) GetPoolByName(ctx context.Context, name string) (store.Pool, error) {
	row, err := s.q.GetPoolByName(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Pool{}, fmt.Errorf("pool %q: %w", name, store.ErrNotFound)
	}
	if err != nil {
		return store.Pool{}, fmt.Errorf("getting pool %q: %w", name, err)
	}
	return toPool(row), nil
}

// ListPools implements store.PoolStore.
func (s *Store) ListPools(ctx context.Context) ([]store.Pool, error) {
	rows, err := s.q.ListPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing pools: %w", err)
	}
	out := make([]store.Pool, 0, len(rows))
	for _, r := range rows {
		out = append(out, toPool(r))
	}
	return out, nil
}

// CreatePoolToken implements store.PoolStore.
func (s *Store) CreatePoolToken(ctx context.Context, poolID, name, hash string, now time.Time) (store.PoolToken, error) {
	id, err := store.NewID()
	if err != nil {
		return store.PoolToken{}, err
	}
	_, err = s.q.InsertPoolToken(ctx, gen.InsertPoolTokenParams{
		ID: id, PoolID: poolID, Name: name, TokenHash: hash, CreatedAt: ts(now),
	})
	if hasCode(err, uniqueViolation) {
		return store.PoolToken{}, fmt.Errorf("pool token: %w", store.ErrConflict)
	}
	if hasCode(err, foreignKeyViolation) {
		return store.PoolToken{}, fmt.Errorf("pool %q: %w", poolID, store.ErrNotFound)
	}
	if err != nil {
		return store.PoolToken{}, fmt.Errorf("creating pool token: %w", err)
	}
	return store.PoolToken{ID: id, PoolID: poolID, Name: name, CreatedAt: now.UTC()}, nil
}

// LookupPoolToken implements store.PoolStore.
func (s *Store) LookupPoolToken(ctx context.Context, hash string) (store.Pool, store.PoolToken, error) {
	r, err := s.q.LookupActivePoolToken(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Pool{}, store.PoolToken{}, fmt.Errorf("pool token: %w", store.ErrNotFound)
	}
	if err != nil {
		return store.Pool{}, store.PoolToken{}, fmt.Errorf("looking up pool token: %w", err)
	}
	pool := toPool(gen.AgentPool{ID: r.PoolID, Name: r.PoolName, Description: r.PoolDescription, CreatedAt: r.PoolCreatedAt})
	return pool, store.PoolToken{
		ID: r.TokenID, PoolID: r.PoolID, Name: r.TokenName,
		CreatedAt: r.TokenCreatedAt.Time.UTC(), LastUsedAt: tsPtr(r.TokenLastUsedAt),
	}, nil
}

// TouchPoolToken implements store.PoolStore.
func (s *Store) TouchPoolToken(ctx context.Context, tokenID string, now time.Time) error {
	n, err := s.q.TouchPoolToken(ctx, gen.TouchPoolTokenParams{UsedAt: ts(now), ID: tokenID})
	if err != nil {
		return fmt.Errorf("touching pool token: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("pool token %q: %w", tokenID, store.ErrNotFound)
	}
	return nil
}

// RevokePoolToken implements store.PoolStore.
func (s *Store) RevokePoolToken(ctx context.Context, tokenID string, now time.Time) error {
	n, err := s.q.RevokePoolTokenCAS(ctx, gen.RevokePoolTokenCASParams{RevokedAt: ts(now), ID: tokenID})
	if err != nil {
		return fmt.Errorf("revoking pool token: %w", err)
	}
	if n > 0 {
		return nil
	}
	c, err := s.q.CountPoolTokens(ctx, tokenID)
	if err != nil {
		return fmt.Errorf("revoking pool token: %w", err)
	}
	if c == 0 {
		return fmt.Errorf("pool token %q: %w", tokenID, store.ErrNotFound)
	}
	return fmt.Errorf("pool token %q already revoked: %w", tokenID, store.ErrStale)
}

// ListPoolTokens implements store.PoolStore.
func (s *Store) ListPoolTokens(ctx context.Context, poolID string) ([]store.PoolToken, error) {
	rows, err := s.q.ListPoolTokens(ctx, poolID)
	if err != nil {
		return nil, fmt.Errorf("listing pool tokens: %w", err)
	}
	out := make([]store.PoolToken, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.PoolToken{
			ID: r.ID, PoolID: r.PoolID, Name: r.Name, CreatedAt: r.CreatedAt.Time.UTC(),
			LastUsedAt: tsPtr(r.LastUsedAt), RevokedAt: tsPtr(r.RevokedAt),
		})
	}
	return out, nil
}
