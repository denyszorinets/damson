package protocol

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// pipeConn is one end of an in-memory Conn pair.
type pipeConn struct {
	in     chan []byte
	out    chan []byte
	done   chan struct{}
	once   sync.Once
	peerCl *pipeConn
}

func newPipe() (*pipeConn, *pipeConn) {
	a2b, b2a := make(chan []byte, 16), make(chan []byte, 16)
	a := &pipeConn{in: b2a, out: a2b, done: make(chan struct{})}
	b := &pipeConn{in: a2b, out: b2a, done: make(chan struct{})}
	a.peerCl, b.peerCl = b, a
	return a, b
}

func (c *pipeConn) ReadFrame(ctx context.Context) ([]byte, error) {
	select {
	case f := <-c.in:
		return f, nil
	case <-c.done:
		return nil, io.EOF
	case <-c.peerCl.done:
		// drain what is already queued first
		select {
		case f := <-c.in:
			return f, nil
		default:
			return nil, io.EOF
		}
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

var errTest = errors.New("test error")
