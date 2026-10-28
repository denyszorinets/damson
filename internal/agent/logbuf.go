package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/denyszorinets/damson/internal/protocol"
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
}

// attemptBuf is the unacknowledged batches of one attempt, oldest first.
// cursor is the index of the first batch not yet sent on this connection.
type attemptBuf struct {
	batches []*batch
	cursor  int
	// gap reports lost entries to the attempt's log (a System line).
	gap func(msg string)
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

	batchBytes atomic.Int64 // flush threshold; the controller may lower it

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
		attempts: map[string]*attemptBuf{},
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

// register creates the buffer of a new attempt. gap is called, outside any
// hub lock, with a message for each run of entries that had to be dropped.
func (h *logHub) register(attempt string, gap func(string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.attempts[attempt] = &attemptBuf{gap: gap}
}

// add stores a batch. It never blocks on the network; call pump to send.
func (h *logHub) add(attempt string, from uint64, entries []protocol.LogEntry) {
	b := &batch{attempt: attempt, from: from, to: from + uint64(len(entries)) - 1, size: estimate(entries), entries: entries}
	var lost []*batch

	h.mu.Lock()
	ab := h.attempts[attempt]
	if ab == nil { // forgotten while the last flush ran
		h.mu.Unlock()
		return
	}
	if h.mem+b.size <= h.memCap {
		h.mem += b.size
	} else {
		lost = h.spillLocked(b)
	}
	dropped := b.entries == nil && b.slot == nil
	if !dropped {
		ab.batches = append(ab.batches, b)
	}
	msgs, reports := h.collectLostLocked(lost)
	report := ab.gap
	h.mu.Unlock()

	for i, msg := range msgs {
		if reports[i] != nil {
			reports[i](msg)
		}
	}
	if dropped {
		h.reportDropped(report, b)
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

// collectLostLocked removes evicted batches from their attempts and returns
// the gap messages with the callbacks to run after unlocking.
func (h *logHub) collectLostLocked(lost []*batch) ([]string, []func(string)) {
	var msgs []string
	var reports []func(string)
	for _, b := range lost {
		ab := h.attempts[b.attempt]
		if ab == nil {
			continue
		}
		for i, x := range ab.batches {
			if x != b {
				continue
			}
			ab.batches = append(ab.batches[:i], ab.batches[i+1:]...)
			if i < ab.cursor {
				ab.cursor--
			}
			break
		}
		b.slot = nil
		msgs = append(msgs, gapMessage(b))
		reports = append(reports, ab.gap)
		h.log.Warn("log spool full; dropped oldest unacknowledged logs", "attempt_id", b.attempt, "seq_from", b.from, "seq_to", b.to)
	}
	return msgs, reports
}

func gapMessage(b *batch) string {
	return fmt.Sprintf("log gap: entries %d-%d were dropped by the agent (log buffer full)", b.from, b.to)
}

func (h *logHub) reportDropped(report func(string), b *batch) {
	h.log.Warn("dropped log batch: spool unavailable", "attempt_id", b.attempt, "seq_from", b.from, "seq_to", b.to)
	if report != nil {
		report(gapMessage(b))
	}
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
			h.removeLocked(ab, ab.cursor)
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

func (h *logHub) removeLocked(ab *attemptBuf, i int) {
	b := ab.batches[i]
	ab.batches = append(ab.batches[:i], ab.batches[i+1:]...)
	if i < ab.cursor {
		ab.cursor--
	}
	h.freeLocked(b)
}

func (h *logHub) freeLocked(b *batch) {
	if b.slot != nil {
		h.sp.release(b.slot)
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
			err := h.link.notify(sctx, protocol.MethodLogAppend, protocol.LogAppend{AttemptID: id, SeqFrom: b.from, Entries: entries})
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
