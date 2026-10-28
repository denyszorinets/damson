package logs

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/protocol"
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
