package logs

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/denyszorinets/damson/internal/store"
)

// Flush writes the pending lines of the execution as chunks and queues the
// resulting log.ack. The completion handler calls it (through Finish) after
// the final step.complete, so every line is persisted before the run advances.
// A stale attempt discards the buffer and is not an error.
func (i *Ingest) Flush(ctx context.Context, executionID string) error {
	st := i.state(executionID)
	if st == nil {
		return nil
	}
	err := i.flushState(ctx, st)
	i.sendAcks(ctx)
	return err
}

// Finish flushes the execution and releases its ring buffer: live-tail
// subscribers see their channel close. If the flush fails the state is kept
// so the caller can retry.
func (i *Ingest) Finish(ctx context.Context, executionID string) error {
	st := i.state(executionID)
	if st == nil {
		return nil
	}
	for try := 0; ; try++ {
		if err := i.flushState(ctx, st); err != nil {
			i.sendAcks(ctx)
			return err
		}
		i.sendAcks(ctx)
		st.mu.Lock()
		if len(st.pending) == 0 {
			st.closeLocked() // nothing arrived since the flush: safe to release
			st.mu.Unlock()
			break
		}
		st.mu.Unlock()
		if try >= 2 {
			return fmt.Errorf("execution %s still receiving logs: %w", executionID, store.ErrConflict)
		}
	}
	i.mu.Lock()
	if i.states[executionID] == st {
		delete(i.states, executionID)
	}
	i.mu.Unlock()
	return nil
}

// flushState persists everything pending in st. Lines stay in st.pending
// until their chunk row commits, so the ack position never runs ahead of the
// database.
func (i *Ingest) flushState(ctx context.Context, st *execState) error {
	st.flushMu.Lock()
	defer st.flushMu.Unlock()
	st.mu.Lock()
	if st.closed || len(st.pending) == 0 {
		st.mu.Unlock()
		return nil
	}
	snap := append([]Line(nil), st.pending...)
	st.mu.Unlock()

	done := 0
	var err error
	for _, run := range contiguousRuns(snap) {
		if err = i.writeRun(ctx, st, run); err != nil {
			break
		}
		done += len(run)
	}

	st.mu.Lock()
	var seq int64
	var ack bool
	if !st.closed { // a concurrent discard already dropped the buffer
		st.pending = append([]Line(nil), st.pending[done:]...)
		st.pendBytes = 0
		for _, l := range st.pending {
			st.pendBytes += len(l.Line) + lineOverhead
		}
		if len(st.pending) > 0 {
			st.oldest = i.cfg.Clock.now()
		}
		seq, ack = st.ackSeq(false)
	}
	agent := st.agentID
	st.mu.Unlock()
	if ack {
		i.queueAck(st.attempt, agent, seq)
	}

	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrStale), errors.Is(err, store.ErrNotFound):
		i.log.Info("discarding logs of stale attempt", "execution_id", st.execID, "attempt_id", st.attempt)
		i.discard(st)
		return nil
	default:
		i.stats.flushErrors.Add(1)
		return fmt.Errorf("writing log chunk: %w", err)
	}
}

// contiguousRuns splits lines at seq holes: a chunk row covers seq_from to
// seq_to, so it must not contain a gap.
func contiguousRuns(lines []Line) [][]Line {
	var runs [][]Line
	start := 0
	for n := 1; n <= len(lines); n++ {
		if n == len(lines) || lines[n].Seq != lines[n-1].Seq+1 {
			runs = append(runs, lines[start:n])
			start = n
		}
	}
	return runs
}

// writeRun stores one contiguous run: object bytes first, then the fenced
// manifest row. A row that already exists means an earlier flush (possibly by
// another controller) persisted at least the start of the run; the persisted
// part is skipped and the rest is written as a new chunk.
func (i *Ingest) writeRun(ctx context.Context, st *execState, run []Line) error {
	for len(run) > 0 {
		err := i.putChunk(ctx, st, run)
		if err == nil {
			return nil
		}
		if !errors.Is(err, store.ErrConflict) {
			return err
		}
		i.stats.chunkConflicts.Add(1)
		existing, err := i.chunkAt(ctx, st.execID, run[0].Seq)
		if err != nil {
			return err
		}
		skip := int(existing.SeqTo - run[0].Seq + 1)
		if skip < 1 || skip > len(run) {
			skip = len(run)
		}
		run = run[skip:]
	}
	return nil
}

func (i *Ingest) putChunk(ctx context.Context, st *execState, run []Line) error {
	from, to := run[0].Seq, run[len(run)-1].Seq
	data, err := EncodeChunk(run)
	if err != nil {
		return err
	}
	key := ObjectKey(st.execID, st.attempt, from, to)
	sctx, cancel := context.WithTimeout(ctx, i.cfg.StoreTimeout)
	defer cancel()
	now := i.cfg.Clock.now()
	if err := i.cfg.Store.PutLogData(sctx, key, data, now); err != nil {
		return fmt.Errorf("storing chunk object: %w", err)
	}
	_, err = i.cfg.Store.InsertLogChunk(sctx, st.attempt, store.NewLogChunk{
		ExecutionID: st.execID, SeqFrom: from, SeqTo: to, Backend: i.cfg.Backend, ObjectKey: key,
		Bytes: int64(len(data)), TsFirst: run[0].TS, TsLast: run[len(run)-1].TS,
	}, now)
	if err != nil {
		return err
	}
	i.stats.chunksWritten.Add(1)
	return nil
}

// chunkAt returns the manifest row that starts at seq.
func (i *Ingest) chunkAt(ctx context.Context, executionID string, seq int64) (store.LogChunk, error) {
	sctx, cancel := context.WithTimeout(ctx, i.cfg.StoreTimeout)
	defer cancel()
	chunks, err := i.cfg.Store.ListLogChunks(sctx, executionID, seq, seq, 64)
	if err != nil {
		return store.LogChunk{}, fmt.Errorf("listing log chunks: %w", err)
	}
	for _, c := range chunks {
		if c.SeqFrom == seq {
			return c, nil
		}
	}
	return store.LogChunk{}, fmt.Errorf("log chunk at seq %d vanished: %w", seq, store.ErrConflict)
}

// duePass flushes executions whose pending lines reached FlushBytes or
// FlushInterval, evicts idle ones and sends the queued acks. It is what the
// loop runs on every tick and wake-up.
func (i *Ingest) duePass(ctx context.Context) {
	now := i.cfg.Clock.now()
	var due []*execState
	var idle []*execState
	i.mu.Lock()
	for _, st := range i.states {
		st.mu.Lock()
		switch {
		case len(st.pending) > 0 && (st.pendBytes >= i.cfg.FlushBytes || now.Sub(st.oldest) >= i.cfg.FlushInterval):
			due = append(due, st)
		case len(st.pending) == 0 && len(st.subs) == 0 && now.Sub(st.lastActive) >= i.cfg.IdleTTL:
			idle = append(idle, st)
		}
		st.mu.Unlock()
	}
	i.mu.Unlock()

	i.flushAll(ctx, due)
	for _, st := range idle {
		i.evict(st)
	}
	i.sendAcks(ctx)
}

// flushAll flushes the states with a bounded number of workers and waits.
func (i *Ingest) flushAll(ctx context.Context, states []*execState) {
	sem := make(chan struct{}, flushWorkers)
	var wg sync.WaitGroup
	for _, st := range states {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := i.flushState(ctx, st); err != nil && ctx.Err() == nil {
				i.log.Error("flushing log chunk", "execution_id", st.execID, "attempt_id", st.attempt, "error", err)
			}
		}()
	}
	wg.Wait()
}

func (i *Ingest) evict(st *execState) {
	st.mu.Lock()
	if len(st.pending) > 0 || len(st.subs) > 0 {
		st.mu.Unlock()
		return
	}
	st.closeLocked()
	st.mu.Unlock()
	i.mu.Lock()
	if i.states[st.execID] == st {
		delete(i.states, st.execID)
	}
	i.mu.Unlock()
	i.stats.executionsEvicted.Add(1)
	i.log.Debug("evicted idle execution log state", "execution_id", st.execID, "attempt_id", st.attempt)
}
