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

var _ store.PoolStore = (*Store)(nil)

// timeLayout is the fixed-width UTC layout of stored timestamps. Fixed width
// keeps text order equal to time order; microseconds match Postgres.
const timeLayout = "2006-01-02T15:04:05.000000Z"

func fmtTime(t time.Time) string { return store.NormalizeTime(t).Format(timeLayout) }

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing stored timestamp %q: %w", s, err)
	}
	return t, nil
}

func parseNullTime(n sql.NullString) (*time.Time, error) {
	if !n.Valid {
		return nil, nil
	}
	t, err := parseTime(n.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func toPool(r gen.AgentPool) (store.Pool, error) {
	at, err := parseTime(r.CreatedAt)
	if err != nil {
		return store.Pool{}, err
	}
	return store.Pool{ID: r.ID, Name: r.Name, Description: r.Description, CreatedAt: at}, nil
}

// CreatePool implements store.PoolStore.
func (s *Store) CreatePool(ctx context.Context, name, description string, now time.Time) (store.Pool, error) {
	id, err := store.NewID()
	if err != nil {
		return store.Pool{}, err
	}
	_, err = s.q.InsertPool(ctx, gen.InsertPoolParams{ID: id, Name: name, Description: description, CreatedAt: fmtTime(now)})
	if isUnique(err) {
		return store.Pool{}, fmt.Errorf("pool %q: %w", name, store.ErrConflict)
	}
	if err != nil {
		return store.Pool{}, fmt.Errorf("creating pool %q: %w", name, err)
	}
	return store.Pool{ID: id, Name: name, Description: description, CreatedAt: store.NormalizeTime(now)}, nil
}

// GetPoolByName implements store.PoolStore.
func (s *Store) GetPoolByName(ctx context.Context, name string) (store.Pool, error) {
	row, err := s.q.GetPoolByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Pool{}, fmt.Errorf("pool %q: %w", name, store.ErrNotFound)
	}
	if err != nil {
		return store.Pool{}, fmt.Errorf("getting pool %q: %w", name, err)
	}
	return toPool(row)
}

// ListPools implements store.PoolStore.
func (s *Store) ListPools(ctx context.Context) ([]store.Pool, error) {
	rows, err := s.q.ListPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing pools: %w", err)
	}
	out := make([]store.Pool, 0, len(rows))
	for _, r := range rows {
		p, err := toPool(r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
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
		ID: id, PoolID: poolID, Name: name, TokenHash: hash, CreatedAt: fmtTime(now),
	})
	if isUnique(err) {
		return store.PoolToken{}, fmt.Errorf("pool token: %w", store.ErrConflict)
	}
	if isForeignKey(err) {
		return store.PoolToken{}, fmt.Errorf("pool %q: %w", poolID, store.ErrNotFound)
	}
	if err != nil {
		return store.PoolToken{}, fmt.Errorf("creating pool token: %w", err)
	}
	return store.PoolToken{ID: id, PoolID: poolID, Name: name, CreatedAt: store.NormalizeTime(now)}, nil
}

// LookupPoolToken implements store.PoolStore.
func (s *Store) LookupPoolToken(ctx context.Context, hash string) (store.Pool, store.PoolToken, error) {
	r, err := s.q.LookupActivePoolToken(ctx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Pool{}, store.PoolToken{}, fmt.Errorf("pool token: %w", store.ErrNotFound)
	}
	if err != nil {
		return store.Pool{}, store.PoolToken{}, fmt.Errorf("looking up pool token: %w", err)
	}
	pool, err := toPool(gen.AgentPool{ID: r.PoolID, Name: r.PoolName, Description: r.PoolDescription, CreatedAt: r.PoolCreatedAt})
	if err != nil {
		return store.Pool{}, store.PoolToken{}, err
	}
	created, err := parseTime(r.TokenCreatedAt)
	if err != nil {
		return store.Pool{}, store.PoolToken{}, err
	}
	used, err := parseNullTime(r.TokenLastUsedAt)
	if err != nil {
		return store.Pool{}, store.PoolToken{}, err
	}
	return pool, store.PoolToken{ID: r.TokenID, PoolID: r.PoolID, Name: r.TokenName, CreatedAt: created, LastUsedAt: used}, nil
}

// TouchPoolToken implements store.PoolStore.
func (s *Store) TouchPoolToken(ctx context.Context, tokenID string, now time.Time) error {
	n, err := s.q.TouchPoolToken(ctx, gen.TouchPoolTokenParams{UsedAt: sql.NullString{String: fmtTime(now), Valid: true}, ID: tokenID})
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
	n, err := s.q.RevokePoolTokenCAS(ctx, gen.RevokePoolTokenCASParams{RevokedAt: sql.NullString{String: fmtTime(now), Valid: true}, ID: tokenID})
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
		created, err := parseTime(r.CreatedAt)
		if err != nil {
			return nil, err
		}
		used, err := parseNullTime(r.LastUsedAt)
		if err != nil {
			return nil, err
		}
		revoked, err := parseNullTime(r.RevokedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, store.PoolToken{ID: r.ID, PoolID: r.PoolID, Name: r.Name, CreatedAt: created, LastUsedAt: used, RevokedAt: revoked})
	}
	return out, nil
}
