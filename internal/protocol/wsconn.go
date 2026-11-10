package protocol

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// WSConn adapts a coder/websocket connection to Conn using binary frames.
type WSConn struct {
	c         *websocket.Conn
	dead      atomic.Bool // the connection failed; skip the close handshake
	closeOnce sync.Once
}

// closeHandshakeTimeout bounds the orderly close handshake.
const closeHandshakeTimeout = time.Second

// NewWSConn wraps c and sets the read limit to MaxFrameSize.
func NewWSConn(c *websocket.Conn) *WSConn {
	c.SetReadLimit(MaxFrameSize)
	return &WSConn{c: c}
}

// ReadFrame returns the next binary message. Text messages are a protocol
// violation and close the connection.
func (w *WSConn) ReadFrame(ctx context.Context) ([]byte, error) {
	typ, b, err := w.c.Read(ctx)
	if err != nil {
		w.drop()
		return nil, fmt.Errorf("reading websocket frame: %w", err)
	}
	if typ != websocket.MessageBinary {
		w.drop()
		return nil, fmt.Errorf("%w: text message", ErrBadFrame)
	}
	return b, nil
}

// WriteFrame sends one binary message of at most MaxFrameSize bytes.
func (w *WSConn) WriteFrame(ctx context.Context, frame []byte) error {
	if len(frame) > MaxFrameSize {
		return fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, len(frame))
	}
	if err := w.c.Write(ctx, websocket.MessageBinary, frame); err != nil {
		w.drop()
		return fmt.Errorf("writing websocket frame: %w", err)
	}
	return nil
}

// drop tears the connection down immediately (read error, write error or
// peer gone): there is nobody to complete a handshake with.
func (w *WSConn) drop() {
	w.dead.Store(true)
	_ = w.c.CloseNow()
}

// Close performs an orderly close handshake bounded by one second, then drops
// the connection. Only the first caller performs the handshake; concurrent and
// later callers return immediately. A connection that already failed is
// dropped without a handshake.
func (w *WSConn) Close() error {
	w.closeOnce.Do(func() {
		if w.dead.Load() {
			_ = w.c.CloseNow()
			return
		}
		t := time.AfterFunc(closeHandshakeTimeout, func() { _ = w.c.CloseNow() })
		defer t.Stop()
		if err := w.c.Close(websocket.StatusNormalClosure, ""); err != nil {
			_ = w.c.CloseNow()
		}
	})
	return nil
}
