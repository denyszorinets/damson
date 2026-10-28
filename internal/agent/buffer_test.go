package agent

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

func newTestSpool(t *testing.T, capacity int64) *spool {
	t.Helper()
	s, err := newSpool(t.TempDir(), capacity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	return s
}

func mustPut(t *testing.T, s *spool, data string) (*spoolSlot, []*spoolSlot) {
	t.Helper()
	sl, ev, err := s.put([]byte(data), nil)
	if err != nil {
		t.Fatal(err)
	}
	return sl, ev
}

func TestSpoolRoundTripAndRelease(t *testing.T) {
	s := newTestSpool(t, 100)
	a, _ := mustPut(t, s, "aaaaaaaaaa")
	b, _ := mustPut(t, s, "bbbbbbbbbb")
	if got, err := s.read(b); err != nil || string(got) != "bbbbbbbbbb" {
		t.Errorf("read b = %q, %v", got, err)
	}
	if s.used() != 20 {
		t.Errorf("used = %d, want 20", s.used())
	}
	s.release(b) // out of order: a is still live, so b's bytes stay counted
	if s.used() != 20 {
		t.Errorf("used after releasing b = %d, want 20 (hole behind a)", s.used())
	}
	s.release(a)
	if s.used() != 0 {
		t.Errorf("used after releasing all = %d", s.used())
	}
	s.release(a) // idempotent
}

func TestSpoolEvictsOldestAndWraps(t *testing.T) {
	s := newTestSpool(t, 50)
	var slots []*spoolSlot
	var evictedAll []*spoolSlot
	for i := range 12 { // 12 x 10 bytes into 50: the oldest seven go
		sl, ev := mustPut(t, s, strings.Repeat(string(rune('a'+i)), 10))
		slots = append(slots, sl)
		evictedAll = append(evictedAll, ev...)
		if s.used() > 50 {
			t.Fatalf("used %d exceeds capacity after put %d", s.used(), i)
		}
	}
	if len(evictedAll) != 7 {
		t.Fatalf("evicted %d, want 7", len(evictedAll))
	}
	for i, sl := range evictedAll {
		if sl != slots[i] {
			t.Errorf("eviction %d is not the oldest in order", i)
		}
	}
	for i := 7; i < 12; i++ { // survivors are intact after wrapping
		got, err := s.read(slots[i])
		if want := strings.Repeat(string(rune('a'+i)), 10); err != nil || string(got) != want {
			t.Errorf("slot %d = %q, %v", i, got, err)
		}
	}
	if fi, err := os.Stat(s.f.Name()); err != nil || fi.Size() > 50 {
		t.Errorf("spool file size = %v, %v; want at most the capacity", fi.Size(), err)
	}
}

func TestSpoolSkipsEndOfFile(t *testing.T) {
	s := newTestSpool(t, 25)
	a, _ := mustPut(t, s, strings.Repeat("a", 10))
	b, _ := mustPut(t, s, strings.Repeat("b", 10))
	// 5 bytes remain at the end: a 10 byte record wraps to the start, which
	// is occupied by a, so a is evicted.
	c, ev := mustPut(t, s, strings.Repeat("c", 10))
	if len(ev) != 1 || ev[0] != a {
		t.Errorf("evicted = %v, want only a", ev)
	}
	if got, _ := s.read(b); string(got) != strings.Repeat("b", 10) {
		t.Errorf("b = %q", got)
	}
	if got, _ := s.read(c); string(got) != strings.Repeat("c", 10) {
		t.Errorf("c = %q", got)
	}
}

func TestSpoolRejectsOversizedRecord(t *testing.T) {
	s := newTestSpool(t, 8)
	if _, _, err := s.put(bytes.Repeat([]byte("x"), 9), nil); !errors.Is(err, errSpoolRecordTooBig) {
		t.Errorf("err = %v", err)
	}
	if sl, _ := mustPut(t, s, "12345678"); sl == nil { // exactly the capacity fits
		t.Error("record of the full capacity rejected")
	}
}

func TestSpoolCloseRemovesFile(t *testing.T) {
	s, err := newSpool(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	name := s.f.Name()
	s.close()
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Errorf("spool file still there: %v", err)
	}
}

// --- log hub ---

func entries(n int, size int) []protocol.LogEntry {
	out := make([]protocol.LogEntry, n)
	for i := range out {
		out[i] = protocol.LogEntry{TS: int64(i), Stream: pluginsdk.Stdout, Line: strings.Repeat("x", size)}
	}
	return out
}

func newTestHub(t *testing.T, memCap, spoolCap int64) *logHub {
	t.Helper()
	h := newLogHub(slog.New(slog.DiscardHandler), &link{}, memCap, spoolCap, t.TempDir())
	t.Cleanup(h.close)
	return h
}

func (h *logHub) memUsed() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.mem
}

func (h *logHub) batchCount(attempt string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ab := h.attempts[attempt]; ab != nil {
		return len(ab.batches)
	}
	return 0
}

func TestHubAckFreesMemoryAndSpool(t *testing.T) {
	h := newTestHub(t, 700, 1<<20)
	h.register("a", nil)
	for i := range 6 { // each batch is about 300 bytes: two fit in memory, the rest spill
		h.add("a", uint64(i*2+1), entries(2, 100))
	}
	if h.memUsed() == 0 || h.memUsed() > 700 {
		t.Errorf("mem = %d, want within (0, 700]", h.memUsed())
	}
	if h.spoolBytes() == 0 {
		t.Error("nothing spilled to the spool")
	}
	h.ack("a", 4) // batches ending at seq 2 and 4
	if h.batchCount("a") != 4 {
		t.Errorf("batches after ack = %d, want 4", h.batchCount("a"))
	}
	h.ack("a", 3) // lower than before: no effect
	if h.batchCount("a") != 4 {
		t.Errorf("a stale ack changed the buffer")
	}
	h.ack("a", 12)
	if h.batchCount("a") != 0 || h.memUsed() != 0 || h.spoolBytes() != 0 {
		t.Errorf("after full ack: batches=%d mem=%d spool=%d", h.batchCount("a"), h.memUsed(), h.spoolBytes())
	}
}

func TestHubForgetFreesEverything(t *testing.T) {
	h := newTestHub(t, 1, 1<<20) // everything goes to the spool
	h.register("a", nil)
	h.add("a", 1, entries(3, 50))
	if h.spoolBytes() == 0 {
		t.Fatal("expected a spooled batch")
	}
	h.forget("a")
	if h.spoolBytes() != 0 || h.batchCount("a") != 0 {
		t.Errorf("spool=%d batches=%d after forget", h.spoolBytes(), h.batchCount("a"))
	}
	h.add("a", 4, entries(1, 10)) // after forget: ignored, no panic
}

func TestHubSpoolOverflowDropsOldestAndReportsGap(t *testing.T) {
	h := newTestHub(t, 1, 1500) // memory is bypassed; the spool holds a few batches
	var gaps []string
	h.register("a", func(m string) { gaps = append(gaps, m) })
	for i := range 20 {
		h.add("a", uint64(i*2+1), entries(2, 100))
		if h.spoolBytes() > 1500 {
			t.Fatalf("spool %d bytes exceeds its cap", h.spoolBytes())
		}
	}
	if len(gaps) == 0 {
		t.Fatal("no gap reported although the spool overflowed")
	}
	if !strings.Contains(gaps[0], "entries 1-2 were dropped") {
		t.Errorf("first gap = %q, want the oldest batch (entries 1-2)", gaps[0])
	}
	h.mu.Lock()
	first := h.attempts["a"].batches[0].from
	h.mu.Unlock()
	if first <= 2 {
		t.Errorf("oldest surviving batch starts at %d; the oldest should be gone", first)
	}
}

func TestHubDropsWhenSpoolUnavailable(t *testing.T) {
	h := newLogHub(slog.New(slog.DiscardHandler), &link{}, 1, 100, "/proc/damson-no-such-dir/x")
	var gaps []string
	h.register("a", func(m string) { gaps = append(gaps, m) })
	h.add("a", 1, entries(2, 100))
	if len(gaps) != 1 || h.batchCount("a") != 0 {
		t.Errorf("gaps=%v batches=%d", gaps, h.batchCount("a"))
	}
}

func TestLogPipeSequencesAndGapLine(t *testing.T) {
	h := newTestHub(t, 1, 600)
	m := newMasker(nil)
	now := func() time.Time { return time.Unix(0, 42) }
	p := newLogPipe(t.Context(), h, "a", m, now, time.Hour, Clock{})
	// Drive flushes by hand: no loop, so the test is deterministic.
	for i := range 10 {
		p.Log(pluginsdk.Stdout, fmt.Sprintf("line %d %s", i, strings.Repeat("y", 150)))
		p.flush()
	}
	p.flush() // delivers the gap line queued by the overflow
	h.mu.Lock()
	var seen []protocol.LogEntry
	var last uint64
	for _, b := range h.attempts["a"].batches {
		if b.from <= last {
			t.Errorf("batch seq %d..%d overlaps or is out of order", b.from, b.to)
		}
		last = b.to
		es, err := h.loadLocked(b)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, es...)
	}
	h.mu.Unlock()
	var gapLines int
	for _, e := range seen {
		if e.Stream == pluginsdk.System && strings.HasPrefix(e.Line, "log gap:") {
			gapLines++
		}
		if e.TS != 42 {
			t.Errorf("entry ts = %d, want the injected clock", e.TS)
		}
	}
	if gapLines == 0 {
		t.Error("the gap was not recorded as a System log line")
	}
}

// --- tombstones ---

func TestTombstones(t *testing.T) {
	var ts tombstones
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ts.add("a", now)
	ts.add("a", now) // idempotent
	if !ts.has("a", now.Add(time.Minute)) || ts.has("b", now) {
		t.Error("has() wrong within the TTL")
	}
	if ts.has("a", now.Add(tombstoneTTL+time.Second)) {
		t.Error("tombstone outlived its TTL")
	}
	for i := range tombstoneMax + 10 {
		ts.add(fmt.Sprintf("t%d", i), now)
	}
	if len(ts.at) > tombstoneMax || ts.has("t0", now) || !ts.has(fmt.Sprintf("t%d", tombstoneMax+9), now) {
		t.Errorf("not bounded or wrong victim: %d entries", len(ts.at))
	}
}
