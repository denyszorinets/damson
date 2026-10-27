package protocol

import (
	"context"
	"fmt"

	"github.com/coder/websocket"
)

// WSConn adapts a coder/websocket connection to Conn using binary frames.
type WSConn struct {
	c *websocket.Conn
}

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
		return nil, fmt.Errorf("reading websocket frame: %w", err)
	}
	if typ != websocket.MessageBinary {
		_ = w.c.CloseNow()
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
		return fmt.Errorf("writing websocket frame: %w", err)
	}
	return nil
}

// Close performs the closing handshake and falls back to dropping the
// connection if the handshake fails.
func (w *WSConn) Close() error {
	if err := w.c.Close(websocket.StatusNormalClosure, ""); err != nil {
		_ = w.c.CloseNow()
	}
	return nil
}
