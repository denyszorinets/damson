package logs

import (
	"sync"
	"time"

	"github.com/denyszorinets/damson/internal/protocol"
)

// ring keeps the newest cap lines.
type ring struct {
	buf  []Line
	head int // index of the oldest line once buf is full
	cap  int
}

func (r *ring) push(l Line) {
	if len(r.buf) < r.cap {
		r.buf = append(r.buf, l)
		return
	}
	r.buf[r.head] = l
	r.head = (r.head + 1) % r.cap
}

// after returns the buffered lines with Seq > seq, oldest first.
func (r *ring) after(seq int64) []Line {
	var out []Line
	for i := range r.buf {
		l := r.buf[(r.head+i)%len(r.buf)]
		if l.Seq > seq {
			out = append(out, l)
		}
	}
	return out
}

type subscriber struct{ ch chan Line }

// execState is everything the controller holds for one running execution.
// It is a cache of the agent's stream, not durable state: after a restart the
// agent resends everything unacknowledged.
type execState struct {
	execID  string
	attempt string

	flushMu sync.Mutex // serializes chunk writes of this execution

	mu         sync.Mutex
	agentID    string
	high       int64 // highest seq accounted for: ingested lines and declared gaps
	acked      int64 // highest seq already queued for log.ack
	pending    []Line
	pendBytes  int
	oldest     time.Time // when the first pending line arrived
	ring       ring
	subs       map[*subscriber]struct{}
	lastActive time.Time
	closed     bool
}

func newExecState(execID, attempt, agentID string, ringLines int, now time.Time) *execState {
	return &execState{
		execID: execID, attempt: attempt, agentID: agentID, ring: ring{cap: ringLines},
		subs: make(map[*subscriber]struct{}), lastActive: now,
	}
}

// applyResult reports what one log.append changed.
type applyResult struct {
	added int // lines newly ingested
	dups  int // lines already ingested
	holes int // lines that skipped ahead without a declared gap
	drops int // subscribers dropped for being too slow
}

// apply ingests m: it drops seqs already seen, counts a declared gap as
// contiguous, and feeds the ring, the pending chunk and the subscribers.
// The caller holds st.mu.
func (st *execState) apply(m protocol.LogAppend, now time.Time) applyResult {
	var res applyResult
	st.lastActive = now
	if m.GapTo >= m.SeqFrom && int64(m.GapTo) > st.high {
		st.high = int64(m.GapTo)
	}
	first := int64(m.FirstEntrySeq())
	for i, e := range m.Entries {
		seq := first + int64(i)
		if seq <= st.high {
			res.dups++
			continue
		}
		if seq > st.high+1 {
			res.holes++ // the agent skipped seqs without declaring a gap: treat as one
		}
		st.high = seq
		l := Line{Seq: seq, TS: time.Unix(0, e.TS).UTC(), Stream: streamName(e.Stream), Line: e.Line}
		if len(st.pending) == 0 {
			st.oldest = now
		}
		st.pending = append(st.pending, l)
		st.pendBytes += len(l.Line) + lineOverhead
		st.ring.push(l)
		res.drops += st.broadcast(l)
		res.added++
	}
	return res
}

// broadcast offers l to every subscriber without blocking and drops the ones
// whose queue is full. It returns how many it dropped. The caller holds mu.
func (st *execState) broadcast(l Line) int {
	dropped := 0
	for s := range st.subs {
		select {
		case s.ch <- l:
		default:
			delete(st.subs, s)
			close(s.ch)
			dropped++
		}
	}
	return dropped
}

// persistedLocked is the highest contiguous seq that is committed or a gap:
// everything below the first pending line, or everything ingested if nothing
// is pending. The caller holds mu.
func (st *execState) persistedLocked() int64 {
	if len(st.pending) > 0 {
		return st.pending[0].Seq - 1
	}
	return st.high
}

// ackSeq returns the seq to acknowledge. Unless force is set, it reports
// false when nothing new is persisted since the last ack. The caller holds mu.
func (st *execState) ackSeq(force bool) (int64, bool) {
	p := st.persistedLocked()
	if p <= 0 || (!force && p <= st.acked) {
		return 0, false
	}
	st.acked = max(st.acked, p)
	return p, true
}

// close ends the live tail. The caller holds mu.
func (st *execState) closeLocked() {
	st.closed = true
	for s := range st.subs {
		close(s.ch)
	}
	st.subs = nil
	st.ring = ring{cap: st.ring.cap}
}

func (st *execState) removeSub(s *subscriber) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.subs[s]; ok {
		delete(st.subs, s)
		close(s.ch)
	}
}
