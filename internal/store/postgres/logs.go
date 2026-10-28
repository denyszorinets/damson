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

var _ store.LogStore = (*Store)(nil)

func toLogChunk(r gen.LogChunk) store.LogChunk {
	return store.LogChunk{
		ID: r.ID, ExecutionID: r.ExecutionID, SeqFrom: r.SeqFrom, SeqTo: r.SeqTo, Backend: r.Backend,
		ObjectKey: r.ObjectKey, Bytes: r.Bytes, TsFirst: r.TsFirst.Time.UTC(), TsLast: r.TsLast.Time.UTC(),
		CreatedAt: r.CreatedAt.Time.UTC(),
	}
}

// InsertLogChunk implements store.LogStore.
func (s *Store) InsertLogChunk(ctx context.Context, attemptID string, c store.NewLogChunk, now time.Time) (store.LogChunk, error) {
	if err := store.ValidateLogChunk(attemptID, c); err != nil {
		return store.LogChunk{}, err
	}
	id, err := store.NewID()
	if err != nil {
		return store.LogChunk{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return store.LogChunk{}, fmt.Errorf("beginning log chunk insert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	// The fence write also locks the row, so the attempt cannot rotate
	// between the check and the insert.
	n, err := q.LockExecutionAttempt(ctx, gen.LockExecutionAttemptParams{ID: c.ExecutionID, AttemptID: attemptID})
	if err != nil {
		return store.LogChunk{}, fmt.Errorf("fencing log chunk for execution %q: %w", c.ExecutionID, err)
	}
	if n == 0 {
		return store.LogChunk{}, s.fencedMiss(ctx, c.ExecutionID, "has a different current attempt")
	}
	_, err = q.InsertLogChunk(ctx, gen.InsertLogChunkParams{
		ID: id, ExecutionID: c.ExecutionID, SeqFrom: c.SeqFrom, SeqTo: c.SeqTo, Backend: c.Backend,
		ObjectKey: c.ObjectKey, Bytes: c.Bytes, TsFirst: ts(c.TsFirst), TsLast: ts(c.TsLast), CreatedAt: ts(now),
	})
	if hasCode(err, uniqueViolation) {
		return store.LogChunk{}, fmt.Errorf("log chunk at seq %d of execution %q: %w", c.SeqFrom, c.ExecutionID, store.ErrConflict)
	}
	if err != nil {
		return store.LogChunk{}, fmt.Errorf("inserting log chunk: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return store.LogChunk{}, fmt.Errorf("committing log chunk insert: %w", err)
	}
	return store.LogChunk{
		ID: id, ExecutionID: c.ExecutionID, SeqFrom: c.SeqFrom, SeqTo: c.SeqTo, Backend: c.Backend,
		ObjectKey: c.ObjectKey, Bytes: c.Bytes, TsFirst: store.NormalizeTime(c.TsFirst), TsLast: store.NormalizeTime(c.TsLast),
		CreatedAt: store.NormalizeTime(now),
	}, nil
}

// ListLogChunks implements store.LogStore.
func (s *Store) ListLogChunks(ctx context.Context, executionID string, fromSeq, toSeq int64, limit int) ([]store.LogChunk, error) {
	rows, err := s.q.ListLogChunks(ctx, gen.ListLogChunksParams{
		ExecutionID: executionID, FromSeq: fromSeq, ToSeq: store.LogRange(toSeq), RowLimit: int32(store.ClampLimit(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("listing log chunks of execution %q: %w", executionID, err)
	}
	out := make([]store.LogChunk, 0, len(rows))
	for _, r := range rows {
		out = append(out, toLogChunk(r))
	}
	return out, nil
}

// PutLogData implements store.LogStore.
func (s *Store) PutLogData(ctx context.Context, key string, data []byte, now time.Time) error {
	if key == "" {
		return fmt.Errorf("empty log data key: %w", store.ErrInvalid)
	}
	if data == nil {
		data = []byte{}
	}
	if err := s.q.PutLogData(ctx, gen.PutLogDataParams{ObjectKey: key, Data: data, CreatedAt: ts(now)}); err != nil {
		return fmt.Errorf("putting log data %q: %w", key, err)
	}
	return nil
}

// GetLogData implements store.LogStore.
func (s *Store) GetLogData(ctx context.Context, key string) ([]byte, error) {
	data, err := s.q.GetLogData(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("log data %q: %w", key, store.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("getting log data %q: %w", key, err)
	}
	return data, nil
}

// DeleteLogDataPrefix implements store.LogStore.
func (s *Store) DeleteLogDataPrefix(ctx context.Context, prefix string) (int64, error) {
	l, err := store.PrefixArgs(prefix)
	if err != nil {
		return 0, err
	}
	n, err := s.q.DeleteLogDataPrefix(ctx, gen.DeleteLogDataPrefixParams{PrefixLen: int32(l), Prefix: prefix})
	if err != nil {
		return 0, fmt.Errorf("deleting log data under %q: %w", prefix, err)
	}
	return n, nil
}
