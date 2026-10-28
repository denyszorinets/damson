package agent

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
	if fi, err := s.f.Stat(); err != nil || fi.Size() > 50 {
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

func TestSpoolHasNoNameOnDisk(t *testing.T) {
	dir := t.TempDir()
	s, err := newSpool(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); s.unlinked && len(left) != 0 {
		t.Errorf("unlinked spool still has files: %v", left)
	}
	if _, _, err := s.put([]byte("data"), nil); err != nil { // still usable
		t.Fatal(err)
	}
	s.close()
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Errorf("files left after close: %v", left)
	}
}

func TestSweepSpoolRemovesStaleFilesOnly(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"damson-spool-1.bin", "damson-spool-2.bin", "other.bin"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := sweepSpool(dir); got != 2 {
		t.Errorf("swept %d, want 2", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "other.bin")); err != nil {
		t.Errorf("unrelated file removed: %v", err)
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
	h.register("a")
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
	h.register("a")
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

func TestHubSpoolOverflowDeclaresGap(t *testing.T) {
	h := newTestHub(t, 1, 1500) // memory is bypassed; the spool holds a few batches
	h.register("a")
	for i := range 20 {
		h.add("a", uint64(i*2+1), entries(2, 100))
		if h.spoolBytes() > 1500 {
			t.Fatalf("spool %d bytes exceeds its cap", h.spoolBytes())
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	bs := h.attempts["a"].batches
	if !bs[0].gap || bs[0].from != 1 {
		t.Fatalf("first batch = %+v; want a gap placeholder starting at 1", bs[0])
	}
	if bs[1].gap || bs[1].from != bs[0].to+1 {
		t.Errorf("batch after the placeholder starts at %d, want %d", bs[1].from, bs[0].to+1)
	}
	line := bs[0].entries[0]
	if line.Stream != pluginsdk.System || !strings.Contains(line.Line, fmt.Sprintf("entries 1-%d were dropped", bs[0].to)) {
		t.Errorf("gap line = %+v", line)
	}
	for i := 1; i < len(bs); i++ {
		if bs[i].gap {
			t.Errorf("adjacent gap placeholders were not merged at %d", i)
		}
	}
}

func TestHubDropsWhenSpoolUnavailable(t *testing.T) {
	h := newLogHub(slog.New(slog.DiscardHandler), &link{}, 1, 100, "/proc/damson-no-such-dir/x")
	h.register("a")
	h.add("a", 1, entries(2, 100))
	h.add("a", 3, entries(2, 100))
	h.mu.Lock()
	defer h.mu.Unlock()
	bs := h.attempts["a"].batches
	if len(bs) != 1 || !bs[0].gap || bs[0].from != 1 || bs[0].to != 4 {
		t.Errorf("batches = %+v; want one merged gap 1-4", bs)
	}
}

func TestLogPipeSequencesAcrossAGap(t *testing.T) {
	h := newTestHub(t, 1, 600)
	h.now = func() time.Time { return time.Unix(0, 42) }
	p := newLogPipe(t.Context(), h, "a", newMasker(nil), func() time.Time { return time.Unix(0, 42) }, time.Hour, Clock{})
	// Drive flushes by hand: no loop, so the test is deterministic.
	for i := range 10 {
		p.Log(pluginsdk.Stdout, fmt.Sprintf("line %d %s", i, strings.Repeat("y", 150)))
		p.flush()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var last uint64
	var gaps int
	for _, b := range h.attempts["a"].batches {
		if b.from != last+1 {
			t.Errorf("batch %d..%d does not follow %d", b.from, b.to, last)
		}
		last = b.to
		if b.gap {
			gaps++
		}
	}
	if gaps == 0 || last != 10 {
		t.Errorf("gaps=%d last=%d; want a declared gap and the stream ending at 10", gaps, last)
	}
}

// --- tombstones ---

func TestTombstones(t *testing.T) {
	var ts tombstones
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ts.add("a", now)
	ts.add("a", now)                     // idempotent
	ts.add("a", now.Add(10*time.Minute)) // refreshes the TTL
	if !ts.has("a", now.Add(20*time.Minute)) {
		t.Error("re-adding did not refresh the TTL")
	}
	ts.add("a", now)
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
