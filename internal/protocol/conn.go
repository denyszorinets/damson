package protocol

import "context"

// Conn is a message-oriented, full-duplex transport. Each frame is one
// encoded Message. Implementations must allow one concurrent ReadFrame and
// concurrent WriteFrame/Close calls, honor ctx cancellation, and make Close
// unblock a pending ReadFrame.
type Conn interface {
	ReadFrame(ctx context.Context) ([]byte, error)
	WriteFrame(ctx context.Context, frame []byte) error
	Close() error
}
