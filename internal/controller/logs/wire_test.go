package logs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
)

// pipeConn is one end of an in-memory protocol.Conn pair.
type pipeConn struct {
	in, out chan []byte
	done    chan struct{}
	once    sync.Once
	other   *pipeConn
}

func newPipe() (*pipeConn, *pipeConn) {
	a2b, b2a := make(chan []byte, 16), make(chan []byte, 16)
	a := &pipeConn{in: b2a, out: a2b, done: make(chan struct{})}
	b := &pipeConn{in: a2b, out: b2a, done: make(chan struct{})}
	a.other, b.other = b, a
	return a, b
}

func (c *pipeConn) ReadFrame(ctx context.Context) ([]byte, error) {
	select {
	case f := <-c.in:
		return f, nil
	case <-c.done:
		return nil, io.EOF
	case <-c.other.done:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *pipeConn) WriteFrame(ctx context.Context, f []byte) error {
	select {
	case c.out <- append([]byte(nil), f...):
		return nil
	case <-c.done:
		return io.ErrClosedPipe
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *pipeConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return nil
}

// TestRegister_LogAppendToLogAckOverTheWire drives the session handler with a
// real protocol peer, as the gateway wires it.
func TestRegister_LogAppendToLogAckOverTheWire(t *testing.T) {
	f := newFx(t, openSQLite(t))
	agentConn, ctlConn := newPipe()
	ctl := protocol.NewPeer(ctlConn, slog.New(slog.DiscardHandler))
	agent := protocol.NewPeer(agentConn, slog.New(slog.DiscardHandler))

	acked := make(chan protocol.LogAck, 4)
	agent.Handle(protocol.MethodLogAck, func(_ context.Context, p protocol.Params) (any, error) {
		var a protocol.LogAck
		if err := p.Decode(&a); err != nil {
			return nil, err
		}
		acked <- a
		return nil, nil
	})
	f.in.cfg.Acks = func(ctx context.Context, _ string, a protocol.LogAck) error {
		return ctl.Notify(ctx, protocol.MethodLogAck, a)
	}
	f.in.Register(ctl, "agent-1")

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, p := range []*protocol.Peer{ctl, agent} {
		wg.Add(1)
		go func() { defer wg.Done(); _ = p.Run(ctx) }()
	}
	t.Cleanup(func() {
		cancel()
		_ = ctl.Close()
		_ = agent.Close()
		wg.Wait()
	})

	require.NoError(t, agent.Notify(ctx, protocol.MethodLogAppend, protocol.LogAppend{
		AttemptID: f.exec.AttemptID, SeqFrom: 1, Entries: entries("one", "two"),
	}))
	waitFor(t, "ingest", func() bool { return f.in.Stats().LinesIngested == 2 })
	assert.Empty(t, acked, "nothing is acknowledged before the chunk commit")

	f.flush()
	got := <-acked
	assert.Equal(t, protocol.LogAck{AttemptID: f.exec.AttemptID, Seq: 2}, got)
}

// Once a batch was refused, later log.append frames already on the socket are
// dropped unacknowledged: ingesting one would let a later ack skip the refused
// batch.
func TestRegister_AfterRefusalLaterBatchesAreDropped(t *testing.T) {
	f := newFx(t, openSQLite(t), func(c *Config) { c.LookupRetries, c.LookupRetryWait = 1, time.Millisecond })
	agentConn, ctlConn := newPipe()
	ctl := protocol.NewPeer(ctlConn, slog.New(slog.DiscardHandler))
	agent := protocol.NewPeer(agentConn, slog.New(slog.DiscardHandler))
	f.in.Register(ctl, "agent-1")

	f.in.cache.forget(f.exec.AttemptID)
	f.in.cfg.Store = failingStore{Store: f.st, getErr: errors.New("db timeout")}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, p := range []*protocol.Peer{ctl, agent} {
		wg.Add(1)
		go func() { defer wg.Done(); _ = p.Run(ctx) }()
	}
	t.Cleanup(func() {
		cancel()
		_ = ctl.Close()
		_ = agent.Close()
		wg.Wait()
	})

	// Batch 1-10 is refused (lookup fails); the database then recovers and 11-20
	// arrives on the same socket before the close takes effect.
	require.NoError(t, agent.Notify(ctx, protocol.MethodLogAppend, protocol.LogAppend{
		AttemptID: f.exec.AttemptID, SeqFrom: 1, Entries: entries(numbered(1, 10)...),
	}))
	waitFor(t, "refusal", func() bool { return f.in.Stats().BatchesRefused == 1 })
	f.in.cfg.Store = f.st
	_ = agent.Notify(ctx, protocol.MethodLogAppend, protocol.LogAppend{
		AttemptID: f.exec.AttemptID, SeqFrom: 11, Entries: entries(numbered(11, 10)...),
	}) // may fail if the close already won; either way nothing may be ingested
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, f.in.Stats().LinesIngested, "nothing from the socket is ingested after a refusal")
	assert.Empty(t, f.acks.all())
}

// A new state starts at the committed frontier read from the database, so a
// batch that does not continue it is refused instead of adopted on trust.
func TestRoute_NewStateStartsAtCommittedFrontier(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFx(t, st)
		for _, r := range [][2]int64{{1, 3}, {4, 6}} { // committed by another controller
			_, err := st.InsertLogChunk(context.Background(), f.exec.AttemptID, store.NewLogChunk{
				ExecutionID: f.exec.ID, SeqFrom: r[0], SeqTo: r[1], Backend: "db", ObjectKey: ObjectKey(f.exec.ID, f.exec.AttemptID, r[0], r[1]),
			}, t0)
			require.NoError(t, err)
		}
		require.ErrorIs(t, f.send(9, 0, "i"), ErrNeedResend, "7-8 are neither committed nor declared")
		f.mustSend(4, 0, "d", "e", "f", "g") // 4-6 are duplicates of the committed chunk, 7 is new
		f.flush()
		assert.EqualValues(t, 7, f.acks.last(f.exec.AttemptID))
		assert.Zero(t, f.in.Stats().ChunkConflicts, "committed lines are recognized as duplicates, not rewritten")
	})
}
