package protocol

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/vmihailenco/msgpack/v5"
)

// ErrAlreadyRunning is returned by a second call to Run on the same Peer.
var ErrAlreadyRunning = errors.New("protocol: peer already running")

// DefaultMaxInFlight is the default bound on concurrently handled incoming
// requests per peer.
const DefaultMaxInFlight = 64

// ErrClosed is returned by calls on a closed Peer, and wraps the cause when
// the connection failed.
var ErrClosed = errors.New("protocol: peer closed")

// Handler serves one incoming method. For a request the returned value (or
// error) is sent as the response; for a notification both are ignored. Return
// an *RPCError to control the error code; any other error becomes
// CodeInternalError.
//
// Notifications are handled one at a time, in arrival order, on the read loop,
// so a notification handler must not block or call the peer. Requests are
// handled concurrently, each in its own goroutine.
//
// A panic in a handler is recovered and logged; the connection stays up and a
// request is answered with CodeInternalError. An error that is not an
// *RPCError is never sent to the remote verbatim (it may carry internal
// detail): the remote sees a generic "internal error" and the detail is
// logged locally with the method and request id.
type Handler func(ctx context.Context, params Params) (result any, err error)

// Peer is one end of a JSON-RPC connection. Both ends can call and notify.
//
// Notification handlers run serially on the read loop. A slow notification
// handler therefore stalls the delivery of every later message on that
// connection, including responses to this peer's own calls. Consumers must
// hand heavy work off quickly (for example, copy a log.append batch into a
// bounded buffer and return).
//
// Incoming requests are handled concurrently, at most MaxInFlight at a time
// (default DefaultMaxInFlight); a request arriving when the limit is reached is
// answered with CodeServerOverloaded without running a handler. Because
// requests are concurrent, they can be processed out of arrival order: a
// consumer must tolerate step.cancel racing or overtaking step.execute for
// the same attempt_id, for example by keeping a tombstone for the attempt_id
// so a late step.execute is rejected.
//
// A Peer runs once: Run may be called a single time.
type Peer struct {
	conn Conn
	log  *slog.Logger

	maxInFlight int64
	inflight    atomic.Int64
	running     atomic.Bool

	wsem   chan struct{} // 1-slot write lock that honors ctx
	nextID atomic.Uint64

	mu       sync.Mutex
	pending  map[uint64]chan *Message
	handlers map[string]Handler

	wg        sync.WaitGroup // request handlers
	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error // set before closed is closed
}

// NewPeer creates a peer over conn. Register handlers, then call Run.
func NewPeer(conn Conn, log *slog.Logger) *Peer {
	if log == nil {
		log = slog.Default()
	}
	return &Peer{
		conn:        conn,
		log:         log,
		maxInFlight: DefaultMaxInFlight,
		wsem:        make(chan struct{}, 1),
		pending:     make(map[uint64]chan *Message),
		handlers:    make(map[string]Handler),
		closed:      make(chan struct{}),
	}
}

// SetMaxInFlight bounds the number of concurrently handled incoming requests.
// It must be called before Run. Values below 1 select DefaultMaxInFlight.
func (p *Peer) SetMaxInFlight(n int) {
	if n < 1 {
		n = DefaultMaxInFlight
	}
	p.maxInFlight = int64(n)
}

// Handle registers h for method, replacing any previous handler.
func (p *Peer) Handle(method string, h Handler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handlers[method] = h
}

// Done is closed when the peer is closed.
func (p *Peer) Done() <-chan struct{} { return p.closed }

// Close closes the connection and fails every pending call with ErrClosed.
// It is safe to call more than once and from any goroutine.
func (p *Peer) Close() error {
	p.closeWith(ErrClosed)
	return nil
}

func (p *Peer) closeWith(cause error) {
	p.closeOnce.Do(func() {
		p.closeErr = cause
		_ = p.conn.Close()
		close(p.closed)
	})
}

func (p *Peer) closedErr() error {
	if errors.Is(p.closeErr, ErrClosed) {
		return p.closeErr
	}
	return fmt.Errorf("%w: %w", ErrClosed, p.closeErr)
}

// Call sends a request and decodes the result into result (which may be nil).
// A JSON-RPC error response is returned as *RPCError.
func (p *Peer) Call(ctx context.Context, method string, params, result any) error {
	id := p.nextID.Add(1)
	ch := make(chan *Message, 1)
	p.mu.Lock()
	p.pending[id] = ch
	p.mu.Unlock()
	defer p.forget(id)

	msg, err := NewRequest(id, method, params)
	if err != nil {
		return err
	}
	if err := p.send(ctx, msg); err != nil {
		return err
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result == nil || len(resp.Result) == 0 {
			return nil
		}
		if err := Decode(resp.Result, result); err != nil {
			return fmt.Errorf("decoding result of %s: %w", method, err)
		}
		return nil
	case <-p.closed:
		return p.closedErr()
	case <-ctx.Done():
		return fmt.Errorf("calling %s: %w", method, ctx.Err())
	}
}

// Notify sends a notification.
func (p *Peer) Notify(ctx context.Context, method string, params any) error {
	msg, err := NewNotification(method, params)
	if err != nil {
		return err
	}
	return p.send(ctx, msg)
}

func (p *Peer) forget(id uint64) {
	p.mu.Lock()
	delete(p.pending, id)
	p.mu.Unlock()
}

func (p *Peer) send(ctx context.Context, m *Message) error {
	frame, err := EncodeMessage(m)
	if err != nil {
		return err
	}
	select {
	case p.wsem <- struct{}{}:
	case <-p.closed:
		return p.closedErr()
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-p.wsem }()
	select {
	case <-p.closed:
		return p.closedErr()
	default:
	}
	if err := p.conn.WriteFrame(ctx, frame); err != nil {
		return fmt.Errorf("sending %s: %w", m.Method, err)
	}
	return nil
}

// Run owns the read loop. It returns when the connection fails, Close is
// called (nil), or ctx is done (ctx.Err()). On return the peer is closed, all
// handlers have finished, and no goroutine started by the peer remains.
func (p *Peer) Run(ctx context.Context) error {
	if !p.running.CompareAndSwap(false, true) {
		return ErrAlreadyRunning
	}
	hctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var cause error
	for {
		select {
		case <-p.closed:
			cause = p.closeErr
		default:
		}
		if cause != nil {
			break
		}
		frame, err := p.conn.ReadFrame(hctx)
		if err != nil {
			cause = err
			break
		}
		msg, err := DecodeMessage(frame)
		if err != nil {
			if errors.Is(err, ErrFrameTooLarge) {
				cause = err
				break
			}
			if errors.Is(err, ErrNullIDResponse) {
				// Never answer a response: two peers would echo errors forever.
				p.log.Debug("dropping response with null id")
				continue
			}
			p.log.Debug("malformed frame", "error", err)
			p.replyError(hctx, nil, &RPCError{Code: CodeParseError, Message: "parse error"})
			continue
		}
		p.route(hctx, msg)
	}
	p.closeWith(cause)
	cancel()
	p.wg.Wait()

	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(p.closeErr, ErrClosed):
		return nil
	default:
		return p.closeErr
	}
}

func (p *Peer) route(ctx context.Context, m *Message) {
	if m.IsResponse() {
		p.deliver(m)
		return
	}
	p.mu.Lock()
	h := p.handlers[m.Method]
	p.mu.Unlock()
	if m.IsNotification() {
		p.notify(ctx, h, m)
		return
	}
	if m.hasNullID() {
		p.replyError(ctx, nil, &RPCError{Code: CodeInvalidRequest, Message: "request id must not be null"})
		return
	}
	if p.inflight.Add(1) > p.maxInFlight {
		p.inflight.Add(-1)
		p.log.Warn("rejecting request: too many in flight", "method", m.Method, "limit", p.maxInFlight)
		p.replyError(ctx, m.ID, &RPCError{Code: CodeServerOverloaded, Message: "server overloaded"})
		return
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer p.inflight.Add(-1)
		p.serve(ctx, h, m)
	}()
}

// notify runs a notification handler on the read loop, containing panics.
func (p *Peer) notify(ctx context.Context, h Handler, m *Message) {
	if h == nil {
		p.log.Debug("ignoring unknown notification", "method", m.Method)
		return
	}
	defer func() {
		// Log only the panic type: the value may carry secrets from handlers.
		if r := recover(); r != nil {
			p.log.Error("notification handler panicked", "method", m.Method, "panic_type", fmt.Sprintf("%T", r), "stack", string(debug.Stack()))
		}
	}()
	if _, err := h(ctx, Params{m.Params}); err != nil {
		p.log.Warn("notification handler failed", "method", m.Method, "error", err)
	}
}

func (p *Peer) serve(ctx context.Context, h Handler, m *Message) {
	defer func() {
		// Log only the panic type: the value may carry secrets from handlers.
		if r := recover(); r != nil {
			p.log.Error("request handler panicked", "method", m.Method, "request_id", idString(m.ID),
				"panic_type", fmt.Sprintf("%T", r), "stack", string(debug.Stack()))
			p.replyError(ctx, m.ID, internalError())
		}
	}()
	if h == nil {
		p.replyError(ctx, m.ID, &RPCError{Code: CodeMethodNotFound, Message: "method not found: " + m.Method})
		return
	}
	res, err := h(ctx, Params{m.Params})
	if err != nil {
		p.replyError(ctx, m.ID, p.errorFor(m, err))
		return
	}
	resp, err := NewResult(m.ID, res)
	if err != nil {
		p.log.Warn("encoding result failed", "method", m.Method, "request_id", idString(m.ID), "error", err)
		p.replyError(ctx, m.ID, internalError())
		return
	}
	p.reply(ctx, resp)
}

func internalError() *RPCError {
	return &RPCError{Code: CodeInternalError, Message: "internal error"}
}

// errorFor maps a handler error to what the remote sees. An *RPCError passes
// through; anything else is logged here and replaced by a generic error.
func (p *Peer) errorFor(m *Message, err error) *RPCError {
	var re *RPCError
	if errors.As(err, &re) {
		if re.cause != nil {
			p.log.Debug("request failed", "method", m.Method, "request_id", idString(m.ID), "error", re.cause)
		}
		return re
	}
	p.log.Warn("request handler failed", "method", m.Method, "request_id", idString(m.ID), "error", err)
	return internalError()
}

func (p *Peer) replyError(ctx context.Context, id msgpack.RawMessage, e *RPCError) {
	p.reply(ctx, NewError(id, e))
}

func (p *Peer) reply(ctx context.Context, m *Message) {
	if err := p.send(ctx, m); err != nil {
		p.log.Debug("sending response failed", "error", err)
	}
}

func (p *Peer) deliver(m *Message) {
	var id uint64
	if len(m.ID) == 0 || Decode(m.ID, &id) != nil {
		p.log.Debug("dropping response with unusable id")
		return
	}
	p.mu.Lock()
	ch := p.pending[id]
	p.mu.Unlock()
	if ch == nil {
		return // caller gave up
	}
	select {
	case ch <- m:
	default:
	}
}
