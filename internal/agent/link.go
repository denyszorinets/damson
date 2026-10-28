package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/denyszorinets/damson/internal/protocol"
)

// errNoLink is returned when a message is sent while no controller connection
// is accepted. Callers keep their data and retry after the next attach.
var errNoLink = errors.New("agent: not connected to the controller")

// link is the executor's handle on the current controller connection. The
// connection changes on every reconnect; the executor outlives them all.
type link struct {
	mu   sync.RWMutex
	peer *protocol.Peer
}

func (l *link) set(p *protocol.Peer) {
	l.mu.Lock()
	l.peer = p
	l.mu.Unlock()
}

// clearIf forgets p unless a newer connection already replaced it.
func (l *link) clearIf(p *protocol.Peer) {
	l.mu.Lock()
	if l.peer == p {
		l.peer = nil
	}
	l.mu.Unlock()
}

func (l *link) current() *protocol.Peer {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.peer
}

func (l *link) notify(ctx context.Context, method string, params any) error {
	p := l.current()
	if p == nil {
		return errNoLink
	}
	return p.Notify(ctx, method, params)
}

func (l *link) call(ctx context.Context, method string, params, result any) error {
	p := l.current()
	if p == nil {
		return errNoLink
	}
	return p.Call(ctx, method, params, result)
}
