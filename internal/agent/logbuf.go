package agent

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// entryOverhead approximates the memory cost of one entry beyond its text.
const entryOverhead = 48

// sendTimeout bounds one log.append write so a stalled socket cannot block
// the log path for long.
const sendTimeout = 10 * time.Second

// batch is a run of consecutive log entries, sent as one log.append. It is
// held in memory (entries) or in the spool (slot) until the controller
// acknowledges it.
type batch struct {
	attempt  string
	from, to uint64
	size     int64
	entries  []protocol.LogEntry
	slot     *spoolSlot
	// gap marks a placeholder for dropped entries from..to. Its single entry
	// is the System line describing the loss and carries sequence number to;
	// it is sent with gap_to = to-1 so the dropped numbers are declared.
	gap bool
}

// attemptBuf is the unacknowledged batches of one attempt, oldest first.
// cursor is the index of the first batch not yet sent on this connection.
type attemptBuf struct {
	batches []*batch
	cursor  int
}

// logHub keeps unacknowledged log batches for all attempts: in memory up to
// memCap, then in the spool, evicting the oldest spooled batches when the
// spool is full (ARCHITECTURE.md section 7.3). It is also the only sender of
// log.append, which keeps batches in order across reconnects.
type logHub struct {
	log      *slog.Logger
	link     *link
	memCap   int64
	spoolCap int64
	spoolDir string

	batchBytes atomic.Int64     // flush threshold; the controller may lower it
	now        func() time.Time // timestamps gap lines

	mu       sync.Mutex
	mem      int64
	sp       *spool
	spErr    error // creating the spool failed; do not retry on every batch
	attempts map[string]*attemptBuf

	sendMu sync.Mutex // serializes pump so batches go out in order
}

func newLogHub(log *slog.Logger, l *link, memCap, spoolCap int64, spoolDir string) *logHub {
	h := &logHub{
		log: log, link: l, memCap: memCap, spoolCap: spoolCap, spoolDir: spoolDir,
		attempts: map[string]*attemptBuf{}, now: time.Now,
	}
	h.batchBytes.Store(DefaultLogBatchBytes)
	return h
}

func estimate(entries []protocol.LogEntry) int64 {
	var n int64
	for _, e := range entries {
		n += int64(len(e.Line)+len(e.Stream)) + entryOverhead
	}
	return n
}

// register creates the buffer of a new attempt.
func (h *logHub) register(attempt string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.attempts[attempt] = &attemptBuf{}
}

// add stores a batch. It never blocks on the network; call pump to send.
// Batches that cannot be kept (evicted from the spool, or spooling failed)
// are replaced in the queue by a gap placeholder, so the loss is declared to
// the controller even when the attempt's pipe has already closed.
func (h *logHub) add(attempt string, from uint64, entries []protocol.LogEntry) {
	b := &batch{attempt: attempt, from: from, to: from + uint64(len(entries)) - 1, size: estimate(entries), entries: entries}
	h.mu.Lock()
	defer h.mu.Unlock()
	ab := h.attempts[attempt]
	if ab == nil { // forgotten while the last flush ran
		return
	}
	var lost []*batch
	if h.mem+b.size <= h.memCap {
		h.mem += b.size
	} else {
		lost = h.spillLocked(b)
	}
	if b.entries == nil && b.slot == nil {
		h.log.Warn("dropped log batch: spool unavailable", "attempt_id", b.attempt, "seq_from", b.from, "seq_to", b.to)
		lost = append(lost, b)
	} else {
		ab.batches = append(ab.batches, b)
	}
	for _, x := range lost {
		h.markLostLocked(x)
	}
}

// spillLocked moves b into the spool. On success b.entries is nil. When the
// spool cannot take it, b is left with neither entries nor slot (dropped).
// It returns the batches evicted to make room.
func (h *logHub) spillLocked(b *batch) []*batch {
	sp := h.spoolLocked()
	if sp == nil {
		b.entries = nil
		return nil
	}
	data, err := protocol.Encode(b.entries)
	if err != nil {
		b.entries = nil
		return nil
	}
	slot, evicted, err := sp.put(data, b)
	var lost []*batch
	for _, sl := range evicted {
		lost = append(lost, sl.owner)
	}
	if err != nil {
		h.log.Warn("spooling logs failed; dropping batch", "error", err)
		b.entries = nil
		return lost
	}
	b.slot, b.entries = slot, nil
	return lost
}

func (h *logHub) spoolLocked() *spool {
	if h.sp != nil || h.spErr != nil {
		return h.sp
	}
	h.sp, h.spErr = newSpool(h.spoolDir, h.spoolCap)
	if h.spErr != nil {
		h.log.Warn("log spool unavailable; logs beyond the memory buffer are dropped", "error", h.spErr)
	}
	return h.sp
}

// markLostLocked replaces b in its attempt's queue (or appends it, when it
// never made it in) by a gap placeholder and merges adjacent placeholders.
func (h *logHub) markLostLocked(b *batch) {
	ab := h.attempts[b.attempt]
	if ab == nil {
		return
	}
	h.log.Warn("log buffer full; dropped unacknowledged logs", "attempt_id", b.attempt, "seq_from", b.from, "seq_to", b.to)
	idx := slices.Index(ab.batches, b)
	g := h.gapBatch(b.attempt, b.from, b.to)
	if idx < 0 {
		ab.batches = append(ab.batches, g)
		idx = len(ab.batches) - 1
	} else {
		h.freeLocked(b) // idempotent for an evicted, already dead, slot
		ab.batches[idx] = g
	}
	h.mem += g.size
	if idx > 0 && ab.batches[idx-1].gap && ab.batches[idx-1].to+1 == g.from {
		h.mergeGapsLocked(ab, idx-1)
		idx--
	}
	if idx+1 < len(ab.batches) && ab.batches[idx+1].gap && g.to+1 == ab.batches[idx+1].from {
		h.mergeGapsLocked(ab, idx)
	}
	ab.cursor = min(ab.cursor, idx) // the placeholder has not been sent
}

// mergeGapsLocked merges the placeholders at i and i+1 into one at i.
func (h *logHub) mergeGapsLocked(ab *attemptBuf, i int) {
	x, y := ab.batches[i], ab.batches[i+1]
	m := h.gapBatch(x.attempt, x.from, y.to)
	h.mem += m.size - x.size - y.size
	ab.batches[i] = m
	ab.batches = slices.Delete(ab.batches, i+1, i+2)
	if ab.cursor > i+1 {
		ab.cursor--
	}
}

func (h *logHub) gapBatch(attempt string, from, to uint64) *batch {
	line := protocol.LogEntry{
		TS: h.now().UnixNano(), Stream: pluginsdk.System,
		Line: fmt.Sprintf("log gap: entries %d-%d were dropped by the agent (log buffer full)", from, to),
	}
	entries := []protocol.LogEntry{line}
	return &batch{attempt: attempt, from: from, to: to, size: estimate(entries), entries: entries, gap: true}
}

// loadLocked returns the batch's entries, reading the spool if needed.
func (h *logHub) loadLocked(b *batch) ([]protocol.LogEntry, error) {
	if b.slot == nil {
		return b.entries, nil
	}
	if h.sp == nil {
		return nil, errSpoolClosed
	}
	raw, err := h.sp.read(b.slot)
	if err != nil {
		return nil, err
	}
	var entries []protocol.LogEntry
	if err := protocol.Decode(raw, &entries); err != nil {
		return nil, fmt.Errorf("decoding spooled batch: %w", err)
	}
	return entries, nil
}

// nextUnsent returns the next batch to send for attempt and advances the cursor.
func (h *logHub) nextUnsent(attempt string) (*batch, []protocol.LogEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ab := h.attempts[attempt]
	for ab != nil && ab.cursor < len(ab.batches) {
		b := ab.batches[ab.cursor]
		entries, err := h.loadLocked(b)
		if err != nil { // unreadable: drop it and carry on
			h.log.Warn("dropping unreadable spooled batch", "attempt_id", attempt, "error", err)
			h.markLostLocked(b)
			continue
		}
		ab.cursor++
		return b, entries
	}
	return nil, nil
}

// unsend steps the cursor back so b is sent again.
func (h *logHub) unsend(b *batch) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ab := h.attempts[b.attempt]; ab != nil && ab.cursor > 0 {
		ab.cursor--
	}
}

func (h *logHub) freeLocked(b *batch) {
	if b.slot != nil {
		if h.sp != nil {
			h.sp.release(b.slot)
		}
		b.slot = nil
		return
	}
	if b.entries != nil {
		h.mem -= b.size
		b.entries = nil
	}
}

// pump sends every batch not yet sent on the current connection, attempt by
// attempt in sequence order. It stops at the first send error: the rest goes
// out after the next reconnect.
func (h *logHub) pump(ctx context.Context) {
	h.sendMu.Lock()
	defer h.sendMu.Unlock()
	for _, id := range h.attemptIDs() {
		for {
			b, entries := h.nextUnsent(id)
			if b == nil {
				break
			}
			sctx, cancel := context.WithTimeout(ctx, sendTimeout)
			la := protocol.LogAppend{AttemptID: id, SeqFrom: b.from, Entries: entries}
			if b.gap && b.to > b.from {
				la.GapTo = b.to - 1
			}
			err := h.link.notify(sctx, protocol.MethodLogAppend, la)
			cancel()
			if err != nil {
				h.unsend(b)
				return
			}
		}
	}
}

func (h *logHub) attemptIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.attempts))
	for id := range h.attempts {
		ids = append(ids, id)
	}
	return ids
}

// resetSent makes the next pump resend everything unacknowledged. Called when
// a new connection is accepted.
func (h *logHub) resetSent() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ab := range h.attempts {
		ab.cursor = 0
	}
}

// ack frees the attempt's batches up to and including seq.
func (h *logHub) ack(attempt string, seq uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ab := h.attempts[attempt]
	if ab == nil {
		return
	}
	n := 0
	for n < len(ab.batches) && ab.batches[n].to <= seq {
		h.freeLocked(ab.batches[n])
		n++
	}
	ab.batches = ab.batches[n:]
	ab.cursor = max(ab.cursor-n, 0)
}

// hasUnsent reports whether the attempt has batches not yet sent on the
// current connection.
func (h *logHub) hasUnsent(attempt string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	ab := h.attempts[attempt]
	return ab != nil && ab.cursor < len(ab.batches)
}

// forget frees everything the attempt still holds.
func (h *logHub) forget(attempt string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ab := h.attempts[attempt]; ab != nil {
		for _, b := range ab.batches {
			h.freeLocked(b)
		}
		delete(h.attempts, attempt)
	}
}

// spoolBytes is the spool size reported in heartbeats.
func (h *logHub) spoolBytes() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sp == nil {
		return 0
	}
	return h.sp.used()
}

// close removes the spool file.
func (h *logHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sp != nil {
		h.sp.close()
		h.sp = nil
	}
	h.spErr = errSpoolClosed
}
