package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

var t0 = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// fakeClock is a manual clock; tickers fire only through tick.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*fakeTicker
}

type fakeTicker struct{ c chan time.Time }

func (f *fakeTicker) C() <-chan time.Time { return f.c }
func (f *fakeTicker) Stop()               {}

func newFakeClock() *fakeClock { return &fakeClock{now: t0} }

func (c *fakeClock) Clock() Clock {
	return Clock{
		Now: func() time.Time {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.now
		},
		NewTicker: func(time.Duration) Ticker {
			c.mu.Lock()
			defer c.mu.Unlock()
			t := &fakeTicker{c: make(chan time.Time, 1)}
			c.tickers = append(c.tickers, t)
			return t
		},
	}
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) tick() {
	c.mu.Lock()
	ts := append([]*fakeTicker(nil), c.tickers...)
	now := c.now
	c.mu.Unlock()
	for _, t := range ts {
		select {
		case t.c <- now:
		default:
		}
	}
}

// ackLog records acks. onAck runs inside the sender, before the ack is recorded.
type ackLog struct {
	mu    sync.Mutex
	acks  []protocol.LogAck
	onAck func(protocol.LogAck)
	err   error
}

func (a *ackLog) send(_ context.Context, _ string, ack protocol.LogAck) error {
	if a.onAck != nil {
		a.onAck(ack)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	a.acks = append(a.acks, ack)
	return nil
}

func (a *ackLog) all() []protocol.LogAck {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]protocol.LogAck(nil), a.acks...)
}

// last is the highest acked seq of attempt, or 0.
func (a *ackLog) last(attempt string) uint64 {
	var seq uint64
	for _, k := range a.all() {
		if k.AttemptID == attempt {
			seq = max(seq, k.Seq)
		}
	}
	return seq
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fx is an Ingest over a real store with one claimed execution.
type fx struct {
	t     *testing.T
	st    store.Store
	in    *Ingest
	clock *fakeClock
	acks  *ackLog
	logs  *syncBuf
	exec  store.Execution
}

func newFx(t *testing.T, st store.Store, opts ...func(*Config)) *fx {
	t.Helper()
	f := &fx{t: t, st: st, clock: newFakeClock(), acks: &ackLog{}, logs: &syncBuf{}}
	f.exec = seedExecution(t, st)
	cfg := Config{
		Store: st, Acks: f.acks.send, Clock: f.clock.Clock(),
		Logger: slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	for _, o := range opts {
		o(&cfg)
	}
	var err error
	f.in, err = New(cfg)
	require.NoError(t, err)
	return f
}

// start runs the loop and stops it at the end of the test.
func (f *fx) start() {
	f.t.Helper()
	require.NoError(f.t, f.in.Start(context.Background()))
	f.t.Cleanup(func() { _ = f.in.Stop(context.Background()) })
}

func seedExecution(t *testing.T, st store.Store) store.Execution {
	t.Helper()
	ctx := context.Background()
	job, ver, err := st.CreateJob(ctx, store.NewJob{
		ProjectID: store.DefaultProjectID, Name: "j", Spec: json.RawMessage(`{"a":1}`), Checksum: "sum",
	}, t0)
	require.NoError(t, err)
	_, err = st.CreateRun(ctx, store.NewRun{
		JobID: job.ID, JobVersionID: ver.ID, Trigger: json.RawMessage(`{"type":"manual"}`), Options: json.RawMessage(`{}`),
		Steps: []store.NewStepInstance{{Path: "main/a", Plugin: "shell", Input: json.RawMessage(`{}`),
			Execution: &store.NewExecution{Pool: "default", ScheduledAt: t0}}},
	}, t0)
	require.NoError(t, err)
	return claimOne(t, st)
}

func claimOne(t *testing.T, st store.Store) store.Execution {
	t.Helper()
	got, err := st.ClaimExecutions(context.Background(), store.ClaimParams{
		AgentID: "agent-1", ControllerID: "ctrl-1", Pools: []string{"default"}, Limit: 1, LeaseTTL: time.Minute,
	}, t0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	return got[0]
}

// reclaim supersedes the execution's attempt with a new one.
func (f *fx) reclaim() store.Execution {
	f.t.Helper()
	require.NoError(f.t, f.st.RequeueExecution(context.Background(), f.exec.ID, f.exec.AttemptID, t0))
	return claimOne(f.t, f.st)
}

func entries(lines ...string) []protocol.LogEntry {
	out := make([]protocol.LogEntry, len(lines))
	for i, l := range lines {
		out[i] = protocol.LogEntry{TS: t0.Add(time.Duration(i)).UnixNano(), Stream: pluginsdk.Stdout, Line: l}
	}
	return out
}

func numbered(from, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "line " + strconv.Itoa(from+i)
	}
	return out
}

// send ingests a batch as the execution's current attempt.
func (f *fx) send(from, gapTo uint64, lines ...string) error {
	return f.sendAs(f.exec.AttemptID, from, gapTo, lines...)
}

func (f *fx) sendAs(attempt string, from, gapTo uint64, lines ...string) error {
	return f.in.Append(context.Background(), "agent-1", protocol.LogAppend{
		AttemptID: attempt, SeqFrom: from, GapTo: gapTo, Entries: entries(lines...),
	})
}

func (f *fx) mustSend(from, gapTo uint64, lines ...string) {
	f.t.Helper()
	require.NoError(f.t, f.send(from, gapTo, lines...))
}

func (f *fx) chunks() []store.LogChunk {
	f.t.Helper()
	cs, err := f.st.ListLogChunks(context.Background(), f.exec.ID, 0, 0, 1000)
	require.NoError(f.t, err)
	return cs
}

// ranges lists the persisted chunk ranges as [from, to] pairs.
func (f *fx) ranges() [][2]int64 {
	f.t.Helper()
	out := [][2]int64{}
	for _, c := range f.chunks() {
		out = append(out, [2]int64{c.SeqFrom, c.SeqTo})
	}
	return out
}

// stored reads every persisted line back through ReadChunk.
func (f *fx) stored() []Line {
	f.t.Helper()
	var out []Line
	for _, c := range f.chunks() {
		data, err := f.st.GetLogData(context.Background(), c.ObjectKey)
		require.NoError(f.t, err)
		lines, err := ReadChunk(data, c.SeqFrom)
		require.NoError(f.t, err)
		out = append(out, lines...)
	}
	return out
}

func (f *fx) flush() {
	f.t.Helper()
	require.NoError(f.t, f.in.Flush(context.Background(), f.exec.ID))
}

func seqs(lines []Line) []int64 {
	out := make([]int64, len(lines))
	for i, l := range lines {
		out[i] = l.Seq
	}
	return out
}
