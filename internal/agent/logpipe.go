package agent

import (
	"context"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

const (
	// maxLineBytes caps one log entry; longer lines are split.
	maxLineBytes = 32 << 10
	// pendingLimitFactor times the batch size is how much unflushed text a
	// producer may queue before Log blocks (backpressure).
	pendingLimitFactor = 8
)

// logPipe turns one attempt's log lines into sequenced batches: masker,
// then a batcher that flushes every flushEvery or batchBytes, whichever comes
// first. Sequence numbers start at 1 and increase by one per entry.
type logPipe struct {
	hub        *logHub
	attempt    string
	m          *masker
	now        func() time.Time
	ctx        context.Context
	flushEvery time.Duration
	clock      Clock

	mu      sync.Mutex
	cond    *sync.Cond
	next    uint64 // seq of the next entry
	from    uint64 // seq of pending[0]
	pending []protocol.LogEntry
	bytes   int
	closed  bool

	kick chan struct{}
	stop chan struct{}
	done chan struct{}
}

func newLogPipe(ctx context.Context, hub *logHub, attempt string, m *masker, now func() time.Time, flushEvery time.Duration, clock Clock) *logPipe {
	p := &logPipe{
		hub: hub, attempt: attempt, m: m, now: now, ctx: ctx, flushEvery: flushEvery, clock: clock,
		next: 1, from: 1,
		kick: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	p.cond = sync.NewCond(&p.mu)
	hub.register(attempt, p.system)
	return p
}

// start launches the flush loop; close stops it.
func (p *logPipe) start() { go p.loop() }

// Log masks line and queues it. It blocks only when the batcher is far
// behind, so a stalled connection slows the plugin instead of exhausting memory.
func (p *logPipe) Log(stream pluginsdk.Stream, line string) {
	line = p.m.Mask(line)
	for len(line) > maxLineBytes {
		cut := maxLineBytes
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		p.add(stream, line[:cut], true)
		line = line[cut:]
	}
	p.add(stream, line, true)
}

// system queues an agent-generated line (already free of secrets).
func (p *logPipe) system(line string) { p.add(pluginsdk.System, line, false) }

func (p *logPipe) add(stream pluginsdk.Stream, line string, throttle bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	limit := int(p.hub.batchBytes.Load()) * pendingLimitFactor
	for throttle && p.bytes >= limit && !p.closed {
		p.cond.Wait()
	}
	if p.closed {
		return
	}
	p.pending = append(p.pending, protocol.LogEntry{TS: p.now().UnixNano(), Stream: stream, Line: line})
	p.next++
	p.bytes += len(line) + entryOverhead
	if p.bytes >= int(p.hub.batchBytes.Load()) {
		select {
		case p.kick <- struct{}{}:
		default:
		}
	}
}

func (p *logPipe) loop() {
	defer close(p.done)
	t := p.clock.ticker(p.flushEvery)
	defer t.Stop()
	for {
		select {
		case <-t.C():
		case <-p.kick:
		case <-p.stop:
			return
		}
		p.flush()
	}
}

// flush moves the pending entries into the hub and sends what it can.
func (p *logPipe) flush() {
	p.mu.Lock()
	entries, from := p.pending, p.from
	p.pending, p.from, p.bytes = nil, p.next, 0
	p.cond.Broadcast()
	p.mu.Unlock()
	limit := int(p.hub.batchBytes.Load())
	for len(entries) > 0 {
		n, size := 0, 0
		for n < len(entries) && (n == 0 || size+len(entries[n].Line)+entryOverhead <= limit) {
			size += len(entries[n].Line) + entryOverhead
			n++
		}
		p.hub.add(p.attempt, from, entries[:n])
		from += uint64(n)
		entries = entries[n:]
	}
	p.hub.pump(p.ctx)
}

// close stops the loop, flushes what is left and rejects later lines.
func (p *logPipe) close() {
	close(p.stop)
	<-p.done
	p.flush()
	p.mu.Lock()
	p.closed = true
	p.cond.Broadcast()
	p.mu.Unlock()
}

// stepEmitter is the pluginsdk.Emitter handed to plugins.
type stepEmitter struct {
	pipe    *logPipe
	m       *masker
	link    *link
	attempt string
	ctx     context.Context
}

func (e *stepEmitter) Log(stream pluginsdk.Stream, line string) { e.pipe.Log(stream, line) }

// Progress forwards a best-effort step.progress notification.
func (e *stepEmitter) Progress(pct float64, msg string) {
	ctx, cancel := context.WithTimeout(e.ctx, sendTimeout)
	defer cancel()
	_ = e.link.notify(ctx, protocol.MethodStepProgress, protocol.StepProgress{
		AttemptID: e.attempt, Percent: min(max(pct, 0), 100), Message: e.m.Mask(msg),
	})
}
