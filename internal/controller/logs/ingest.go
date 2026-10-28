package logs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
)

// ErrNoLiveTail is returned by Subscribe when this controller holds no
// buffer for the execution.
var ErrNoLiveTail = api.ErrNoLiveTail

var _ api.LogTail = (*Ingest)(nil)

// ErrNeedResend means the batch was not (fully) ingested and the agent must
// send it again. log.append is a notification, so the controller cannot say
// so on the wire; Register closes the session instead, and the agent resends
// everything after its last acknowledgement when it reconnects.
var ErrNeedResend = errors.New("logs: batch not ingested, the agent must resend")

// ErrStopped is returned for appends that arrive once Stop has begun.
var ErrStopped = errors.New("logs: ingest stopped")

// maxFloors bounds the memory of acknowledged positions kept per attempt.
const maxFloors = 8192

// maxSeq bounds sequence numbers so int64 arithmetic cannot overflow.
const maxSeq = math.MaxInt64 / 4

// Stats are monotonic counters, the hook for metrics (ARCHITECTURE section 17).
type Stats struct {
	LinesIngested       int64 // lines accepted into ring and pending chunks
	LinesDuplicate      int64 // lines dropped because their seq was already ingested
	BatchesOutOfOrder   int64 // batches that skipped seqs without a declared gap (session closed, agent resends)
	BatchesRefused      int64 // batches refused for a transient error or the pending cap (session closed)
	StaleBatches        int64 // log.append batches discarded by fencing
	ChunksWritten       int64
	ChunkConflicts      int64 // chunk rows that already existed (absorbed as persisted)
	SubscribersDropped  int64 // live-tail subscribers dropped for being too slow
	AcksSent            int64
	FlushErrors         int64
	ExecutionsEvicted   int64 // idle executions removed without Finish
	LinesDiscardedStale int64 // buffered lines dropped when the store reported a stale attempt
}

type counters struct {
	linesIngested, linesDuplicate, batchesOutOfOrder, batchesRefused, staleBatches, chunksWritten atomic.Int64
	chunkConflicts, subscribersDropped, acksSent, flushErrors                                     atomic.Int64
	executionsEvicted, linesDiscardedStale                                                        atomic.Int64
}

type pendingAck struct {
	agentID string
	seq     int64
}

// Ingest is the controller's log.append handler, ring buffer and chunk
// writer. Create it with New, then Start it.
type Ingest struct {
	cfg   Config
	log   *slog.Logger
	cache *attemptCache
	stats counters

	mu     sync.Mutex
	states map[string]*execState // by execution id
	acks   map[string]pendingAck // by attempt id; coalesced until the next send
	// floors remembers, per attempt, the highest seq acknowledged to the agent
	// (bounded). A state created again for the same attempt starts there, never
	// from the first batch it happens to see, so lines dropped in between can
	// not be skipped by a later ack.
	floors     map[string]int64
	floorOrder []string

	lifeMu  sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	wake    chan struct{}
}

// New validates cfg. Nothing runs until Start.
func New(cfg Config) (*Ingest, error) {
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}
	return &Ingest{
		cfg: cfg, log: cfg.Logger, cache: newAttemptCache(attemptCacheSize, cfg.AttemptTTL),
		states: make(map[string]*execState), acks: make(map[string]pendingAck), floors: make(map[string]int64), wake: make(chan struct{}, 1),
	}, nil
}

// Stats returns a snapshot of the counters.
func (i *Ingest) Stats() Stats {
	return Stats{
		LinesIngested: i.stats.linesIngested.Load(), LinesDuplicate: i.stats.linesDuplicate.Load(),
		BatchesOutOfOrder: i.stats.batchesOutOfOrder.Load(), BatchesRefused: i.stats.batchesRefused.Load(), StaleBatches: i.stats.staleBatches.Load(),
		ChunksWritten: i.stats.chunksWritten.Load(), ChunkConflicts: i.stats.chunkConflicts.Load(),
		SubscribersDropped: i.stats.subscribersDropped.Load(), AcksSent: i.stats.acksSent.Load(),
		FlushErrors: i.stats.flushErrors.Load(), ExecutionsEvicted: i.stats.executionsEvicted.Load(),
		LinesDiscardedStale: i.stats.linesDiscardedStale.Load(),
	}
}

// Register installs the log.append handler for one agent socket. The gateway
// calls it when it creates the session, next to the hello and heartbeat
// handlers. When a batch cannot be ingested the session is closed so that the
// agent reconnects and resends from its last acknowledgement.
func (i *Ingest) Register(peer *protocol.Peer, agentID string) {
	h := i.Handler(agentID)
	peer.Handle(protocol.MethodLogAppend, func(ctx context.Context, p protocol.Params) (any, error) {
		res, err := h(ctx, p)
		if errors.Is(err, ErrNeedResend) {
			i.log.Warn("closing agent session so it resends logs", "agent_id", agentID, "error", err)
			go func() { _ = peer.Close() }() // not on the read loop: Close waits for it
			return nil, nil
		}
		return res, err
	})
}

// Handler returns the log.append handler for agentID. log.append is a
// notification, so the result is ignored; acknowledgements go out as separate
// log.ack notifications through Config.Acks.
func (i *Ingest) Handler(agentID string) protocol.Handler {
	return func(ctx context.Context, p protocol.Params) (any, error) {
		var m protocol.LogAppend
		if err := p.Decode(&m); err != nil {
			return nil, err
		}
		return nil, i.Append(ctx, agentID, m)
	}
}

func (i *Ingest) isStopped() bool {
	i.lifeMu.Lock()
	defer i.lifeMu.Unlock()
	return i.stopped
}

// Append ingests one decoded log.append from agentID. It returns
// ErrNeedResend when it kept less than the batch holds for a reason other
// than a stale attempt (transient store error, a hole in the sequence, the
// pending cap); nothing past the last ingested line is ever acknowledged.
func (i *Ingest) Append(ctx context.Context, agentID string, m protocol.LogAppend) error {
	if m.AttemptID == "" || m.SeqFrom == 0 || m.SeqFrom > maxSeq || m.GapTo > maxSeq {
		return &protocol.RPCError{Code: protocol.CodeInvalidParams, Message: "log.append needs attempt_id and a seq_from of at least 1"}
	}
	if i.isStopped() {
		return ErrStopped
	}
	for try := 0; ; try++ {
		st, err := i.routeWithRetry(ctx, agentID, m.AttemptID)
		switch {
		case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrStale):
			i.stats.staleBatches.Add(1)
			i.log.Debug("discarding log batch of stale attempt", "attempt_id", m.AttemptID, "agent_id", agentID)
			i.queueAck(m.AttemptID, agentID, int64(m.LastSeq())) // nothing will ever be kept: let the agent free it
			return nil
		case err != nil:
			i.stats.batchesRefused.Add(1)
			return fmt.Errorf("%w: resolving attempt: %w", ErrNeedResend, err)
		}
		st.mu.Lock()
		if st.closed { // finished or evicted between route and here: take the fresh state
			st.mu.Unlock()
			if try < 2 {
				continue
			}
			i.stats.batchesRefused.Add(1)
			return fmt.Errorf("%w: state closed", ErrNeedResend)
		}
		if st.pendBytes >= i.cfg.MaxPendingBytes {
			st.mu.Unlock()
			i.stats.batchesRefused.Add(1)
			return fmt.Errorf("%w: %d bytes pending, store not keeping up", ErrNeedResend, st.pendBytes)
		}
		st.agentID = agentID
		res := st.apply(m, i.cfg.Clock.now())
		var ackSeq int64
		var ack bool
		if res.added == 0 && !res.outOfOrder { // duplicates and gaps: the agent may have lost our ack
			ackSeq, ack = st.ackSeq(true)
		}
		big := st.pendBytes >= i.cfg.FlushBytes
		st.mu.Unlock()

		i.stats.linesIngested.Add(int64(res.added))
		i.stats.linesDuplicate.Add(int64(res.dups))
		i.stats.subscribersDropped.Add(int64(res.drops))
		if ack {
			i.queueAck(m.AttemptID, agentID, ackSeq)
		}
		if big {
			select {
			case i.wake <- struct{}{}:
			default:
			}
		}
		if res.outOfOrder {
			i.stats.batchesOutOfOrder.Add(1)
			return fmt.Errorf("%w: batch from seq %d starts beyond the contiguous frontier", ErrNeedResend, m.SeqFrom)
		}
		return nil
	}
}

// routeWithRetry resolves the attempt, retrying a transient store error a
// few times before giving up (the caller then asks the agent to resend).
func (i *Ingest) routeWithRetry(ctx context.Context, agentID, attempt string) (*execState, error) {
	for try := 0; ; try++ {
		st, err := i.route(ctx, agentID, attempt)
		if err == nil || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrStale) || try >= i.cfg.LookupRetries {
			return st, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(i.cfg.LookupRetryWait):
		}
	}
}

// route finds or creates the state of the execution that owns attempt. A
// cached lookup is trusted for at most AttemptTTL. When another attempt
// holds the buffer the store decides which one is current before the buffer
// is replaced, so a late batch of a superseded attempt can never discard the
// current attempt's lines.
func (i *Ingest) route(ctx context.Context, agentID, attempt string) (*execState, error) {
	execID, cached := i.cachedExec(attempt)
	verified := false
	for try := 0; try < 4; try++ {
		if cached && execID == "" {
			return nil, store.ErrStale
		}
		if !cached {
			var err error
			if execID, err = i.lookup(ctx, attempt); err != nil {
				return nil, err
			}
		}
		i.mu.Lock()
		old := i.states[execID]
		if old != nil && old.attempt == attempt {
			i.mu.Unlock()
			return old, nil
		}
		if old != nil && !verified {
			i.mu.Unlock()
			if err := i.verifyCurrent(ctx, execID, attempt); err != nil {
				return nil, err
			}
			verified = true
			continue
		}
		floor := int64(-1)
		if f, ok := i.floors[attempt]; ok {
			floor = f
		}
		st := newExecState(execID, attempt, agentID, floor, i.cfg.RingLines, i.cfg.Clock.now())
		i.states[execID] = st
		i.mu.Unlock()
		if old != nil {
			i.discard(old)
		}
		return st, nil
	}
	return nil, fmt.Errorf("attempt %s keeps changing owner: %w", attempt, store.ErrConflict)
}

// verifyCurrent asks the store whether attempt is still the execution's
// current one; if not, it is stale and the answer is cached for a while.
func (i *Ingest) verifyCurrent(ctx context.Context, execID, attempt string) error {
	sctx, cancel := context.WithTimeout(ctx, i.cfg.StoreTimeout)
	defer cancel()
	e, err := i.cfg.Store.GetExecution(sctx, execID)
	if err != nil {
		return err
	}
	if e.AttemptID != attempt {
		i.cache.put(attempt, "", i.cfg.Clock.now())
		return store.ErrStale
	}
	return nil
}

func (i *Ingest) cachedExec(attempt string) (string, bool) {
	return i.cache.get(attempt, i.cfg.Clock.now())
}

// lookup asks the store which execution attempt belongs to and caches the
// answer. ErrNotFound means the attempt is stale or unknown.
func (i *Ingest) lookup(ctx context.Context, attempt string) (string, error) {
	sctx, cancel := context.WithTimeout(ctx, i.cfg.StoreTimeout)
	defer cancel()
	e, err := i.cfg.Store.GetExecutionByAttempt(sctx, attempt)
	if errors.Is(err, store.ErrNotFound) {
		i.cache.put(attempt, "", i.cfg.Clock.now())
		return "", err
	}
	if err != nil {
		return "", err
	}
	i.cache.put(attempt, e.ID, i.cfg.Clock.now())
	return e.ID, nil
}

// Invalidate forgets what is cached about attempt. Dispatch calls it when it
// moves an execution to a new attempt, so the old one is fenced at once.
func (i *Ingest) Invalidate(attempt string) { i.cache.forget(attempt) }

// discard drops a state whose attempt is stale: its lines are never written.
func (i *Ingest) discard(st *execState) {
	st.mu.Lock()
	i.stats.linesDiscardedStale.Add(int64(len(st.pending)))
	st.pending, st.pendBytes = nil, 0
	st.closeLocked()
	st.mu.Unlock()
	i.mu.Lock()
	if i.states[st.execID] == st {
		delete(i.states, st.execID)
	}
	i.mu.Unlock()
	i.cache.forget(st.attempt)
}

func (i *Ingest) state(executionID string) *execState {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.states[executionID]
}

// queueAck records an ack to send at the next pass; later acks of the same
// attempt replace earlier ones.
func (i *Ingest) queueAck(attempt, agentID string, seq int64) {
	if seq <= 0 {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if cur, ok := i.acks[attempt]; !ok || seq >= cur.seq {
		i.acks[attempt] = pendingAck{agentID: agentID, seq: seq}
	}
	i.setFloorLocked(attempt, seq)
}

// setFloorLocked records the highest seq acknowledged for attempt, evicting
// the oldest remembered attempt beyond maxFloors. The caller holds i.mu.
func (i *Ingest) setFloorLocked(attempt string, seq int64) {
	if cur, ok := i.floors[attempt]; ok {
		i.floors[attempt] = max(cur, seq)
		return
	}
	if len(i.floorOrder) >= maxFloors {
		delete(i.floors, i.floorOrder[0])
		i.floorOrder = i.floorOrder[1:]
	}
	i.floors[attempt] = seq
	i.floorOrder = append(i.floorOrder, attempt)
}

// sendAcks delivers the coalesced acks. Delivery failures are dropped: the
// agent resends unacked batches after reconnecting, and duplicates re-ack.
func (i *Ingest) sendAcks(ctx context.Context) {
	i.mu.Lock()
	batch := i.acks
	i.acks = make(map[string]pendingAck)
	i.mu.Unlock()
	for attempt, a := range batch {
		sctx, cancel := context.WithTimeout(ctx, i.cfg.StoreTimeout)
		err := i.cfg.Acks(sctx, a.agentID, protocol.LogAck{AttemptID: attempt, Seq: uint64(a.seq)})
		cancel()
		if err != nil {
			i.log.Debug("sending log.ack failed", "attempt_id", attempt, "agent_id", a.agentID, "error", err)
			continue
		}
		i.stats.acksSent.Add(1)
	}
}
