package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/store"
)

// LogLine is one log entry as served to clients.
type LogLine struct {
	Seq    int64     `json:"seq"`
	TS     time.Time `json:"ts,omitzero"`
	Stream string    `json:"stream" enum:"out,err,sys"`
	Line   string    `json:"line"`
}

// LogGap is a range of sequence numbers that will never be delivered: the
// agent declared it lost (spool overflow) or its stored chunk is missing.
type LogGap struct {
	SeqFrom int64 `json:"seq_from"`
	SeqTo   int64 `json:"seq_to"`
}

// LogEnd closes a log stream once the execution is terminal and every stored
// line was sent.
type LogEnd struct {
	State   string `json:"state"`
	LastSeq int64  `json:"last_seq"`
}

// ErrNoLiveTail is returned by LogTail.Subscribe when this controller has no
// live buffer for the execution; the stream then polls the store.
var ErrNoLiveTail = errors.New("no live log tail for this execution")

// LogTail is the live part of the log read path (ARCHITECTURE section 10): the
// owning controller's ring buffer, proxied if the execution runs elsewhere.
// Subscribe replays buffered lines with seq > afterSeq, then delivers new ones
// until ctx ends or the execution finishes (channel closed).
type LogTail interface {
	Subscribe(ctx context.Context, executionID string, afterSeq int64) (<-chan LogLine, error)
}

// ExecutionDetail is an execution with the step it belongs to.
type ExecutionDetail struct {
	Execution
	RunID    string `json:"run_id"`
	StepPath string `json:"step_path"`
	Plugin   string `json:"plugin,omitempty"`
}

type executionInput struct {
	ID string `path:"id" maxLength:"128"`
}

type executionOutput struct{ Body ExecutionDetail }

type logsInput struct {
	ID          string `path:"id" maxLength:"128"`
	AfterSeq    int64  `query:"after_seq" minimum:"0" doc:"Only lines with a greater sequence number."`
	LastEventID string `header:"Last-Event-ID" doc:"Sent by EventSource on reconnect; takes precedence over after_seq."`
}

func registerExecutions(api huma.API, d deps) {
	registerRunEvents(api, d)
	huma.Register(api, huma.Operation{
		OperationID: "get-execution", Method: http.MethodGet, Path: "/executions/{id}",
		Summary: "Get one execution attempt", Tags: []string{"executions"}, Errors: []int{http.StatusNotFound},
	}, func(ctx context.Context, in *executionInput) (*executionOutput, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		e, err := d.ds.GetExecution(ctx, in.ID)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		step, err := d.ds.GetStepInstance(ctx, e.StepInstanceID)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		return &executionOutput{Body: ExecutionDetail{
			Execution: toExecution(e), RunID: e.RunID, StepPath: step.Path, Plugin: step.Plugin,
		}}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "stream-execution-logs", Method: http.MethodGet, Path: "/executions/{id}/logs",
		Summary: "Server-sent events of an execution's log", Tags: []string{"executions"}, Errors: []int{http.StatusNotFound, http.StatusServiceUnavailable},
		Description: "Stored chunks first, in sequence order, then the live tail, then end. Events: log {seq, ts, stream, line} with id = seq; gap {seq_from, seq_to} for a range that will never arrive (declared lost by the agent, or a missing stored chunk); end {state, last_seq}.",
		Responses:   sseOperationResponses("An event stream."),
	}, func(ctx context.Context, in *logsInput) (*huma.StreamResponse, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		if _, err := d.ds.GetExecution(ctx, in.ID); err != nil {
			return nil, apiError(ctx, err)
		}
		after := in.AfterSeq
		if n, ok := parseEventID(in.LastEventID); ok {
			after = n
		}
		release, err := d.acquireStream()
		if err != nil {
			return nil, apiError(ctx, err)
		}
		return sseResponse(d, release, func(ctx context.Context, s *sseWriter) {
			(&logStream{d: d, s: s, execID: in.ID, next: after + 1}).run(ctx)
		}), nil
	})
}

// logStream sends one execution's log. next is the next sequence number the
// client has not seen.
//
// A hole in the stored sequence (the next chunk starts beyond next) is not
// reported at once: chunks may commit out of order, so the missing lines can
// still arrive. A gap event is sent only when the hole is confirmed, that is
// when the execution is terminal (nothing more will be written) or the same
// hole was seen on an earlier poll too. Declared gaps (the agent's gap_to)
// look the same in the manifest and are confirmed the same way.
type logStream struct {
	d      deps
	s      *sseWriter
	execID string
	next   int64

	pass      int  // poll counter; a hole is confirmed by a later pass
	terminal  bool // the execution had finished when this pass began
	blocked   bool // drainStored stopped at an unconfirmed hole
	hole      *holeObs
	held      []LogLine // live lines waiting behind an unconfirmed hole
	lastStamp store.LogStamp
	haveStamp bool
}

// holeObs records the first pass that saw a hole.
type holeObs struct {
	gap  LogGap
	pass int
}

const (
	chunkPage = 100
	// maxHeld bounds the live lines kept behind an unconfirmed hole; beyond
	// it the hole is reported so memory stays bounded.
	maxHeld = 5000
)

func (l *logStream) run(ctx context.Context) {
	var live <-chan LogLine
	if l.d.tail != nil {
		ch, err := l.d.tail.Subscribe(ctx, l.execID, l.next-1)
		switch {
		case err == nil:
			live = ch
		case !errors.Is(err, ErrNoLiveTail) && ctx.Err() == nil:
			loggerFromCtx(ctx).Warn("log tail unavailable, polling", "execution_id", l.execID, "error", err)
		}
	}
	beat := time.NewTicker(l.d.stream.Heartbeat)
	defer beat.Stop()
	for {
		l.pass++
		if cont, ok := l.poll(ctx); !ok || !cont {
			return
		}
		timer := time.NewTimer(jitter(l.d.stream.PollInterval))
		ok := l.wait(ctx, &live, timer.C, beat.C)
		timer.Stop()
		if !ok {
			return
		}
	}
}

// poll does one store pass: the cheap stamp first, and the chunk list only
// when the stamp moved or a hole awaits its second look. The stamp (hence the
// state) is read before the chunks, so lines flushed before the execution
// became terminal are never missed. ok is false when the stream is over; cont
// is false after the end event.
func (l *logStream) poll(ctx context.Context) (cont, ok bool) {
	ls, err := l.d.ds.GetLogStamp(ctx, l.execID)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, store.ErrNotFound) {
			return false, false
		}
		loggerFromCtx(ctx).Warn("log stream: reading execution", "execution_id", l.execID, "error", err)
		return true, true
	}
	if l.haveStamp && ls == l.lastStamp && l.hole == nil && len(l.held) == 0 {
		return true, true
	}
	l.lastStamp, l.haveStamp = ls, true
	l.terminal = ls.State.Terminal()
	if !l.drainStored(ctx) || !l.flushHeld() {
		return false, false
	}
	if l.terminal {
		_ = l.s.event("", "end", LogEnd{State: string(ls.State), LastSeq: l.next - 1})
		return false, true
	}
	return true, true
}

// wait blocks until the next poll tick, forwarding live lines and heartbeats
// meanwhile. It reports false when the stream is over.
func (l *logStream) wait(ctx context.Context, live *<-chan LogLine, poll, beat <-chan time.Time) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-beat:
			if l.s.comment("heartbeat") != nil {
				return false
			}
		case <-poll:
			return true
		case ln, ok := <-*live:
			if !ok {
				*live = nil
				return true
			}
			if !l.sendLive(ctx, ln) {
				return false
			}
		}
	}
}

// sendLive queues a live line and forwards what is deliverable. A line ahead
// of next means lines between are not in the tail: the store is read, and
// what is still missing is held back until the hole is confirmed.
func (l *logStream) sendLive(ctx context.Context, ln LogLine) bool {
	if ln.Seq < l.next {
		return true
	}
	l.held = append(l.held, ln)
	if ln.Seq > l.next && !l.drainStored(ctx) {
		return false
	}
	return l.flushHeld()
}

// flushHeld sends held live lines in order, stopping at an unconfirmed hole.
func (l *logStream) flushHeld() bool {
	i := 0
	for ; i < len(l.held); i++ {
		ln := l.held[i]
		if ln.Seq < l.next {
			continue
		}
		if ln.Seq > l.next {
			if !l.confirmed(l.next, ln.Seq-1) {
				break
			}
			if !l.gap(l.next, ln.Seq-1) {
				return false
			}
		}
		if !l.line(ln) {
			return false
		}
	}
	l.held = l.held[i:]
	return true
}

// confirmed reports whether the hole [from, to] is real: the execution is
// over, too many lines wait behind it, or an earlier pass saw it too.
func (l *logStream) confirmed(from, to int64) bool {
	g := LogGap{SeqFrom: from, SeqTo: to}
	if l.terminal || len(l.held) > maxHeld {
		return true
	}
	if l.hole != nil && l.hole.gap == g && l.hole.pass < l.pass {
		return true
	}
	if l.hole == nil || l.hole.gap != g {
		l.hole = &holeObs{gap: g, pass: l.pass}
	}
	return false
}

func (l *logStream) line(ln LogLine) bool {
	if l.s.event(strconv.FormatInt(ln.Seq, 10), "log", ln) != nil {
		return false
	}
	l.next, l.hole = ln.Seq+1, nil
	return true
}

func (l *logStream) gap(from, to int64) bool {
	if l.s.event(strconv.FormatInt(to, 10), "gap", LogGap{SeqFrom: from, SeqTo: to}) != nil {
		return false
	}
	l.next, l.hole = to+1, nil
	return true
}

// drainStored sends every stored line from next on, in sequence order, and
// stops at a hole that is not confirmed yet. It reports false when the stream
// is over.
func (l *logStream) drainStored(ctx context.Context) bool {
	l.blocked = false
	for !l.blocked {
		chunks, err := l.d.ds.ListLogChunks(ctx, l.execID, l.next, 0, chunkPage)
		if err != nil {
			if ctx.Err() == nil {
				loggerFromCtx(ctx).Warn("log stream: listing chunks", "execution_id", l.execID, "error", err)
			}
			return ctx.Err() == nil
		}
		for _, c := range chunks {
			if c.SeqTo < l.next {
				continue
			}
			if !l.sendChunk(ctx, c) {
				return false
			}
			if l.blocked {
				return true
			}
		}
		if len(chunks) < chunkPage {
			return true
		}
	}
	return true
}

func (l *logStream) sendChunk(ctx context.Context, c store.LogChunk) bool {
	if c.SeqFrom > l.next {
		if !l.confirmed(l.next, c.SeqFrom-1) {
			l.blocked = true
			return true
		}
		if !l.gap(l.next, c.SeqFrom-1) {
			return false
		}
	}
	lines, err := l.readChunk(ctx, c)
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		// The manifest row is committed after the object, so a missing or
		// unreadable object is a loss, not a race.
		loggerFromCtx(ctx).Warn("log stream: chunk unreadable", "execution_id", l.execID, "seq_from", c.SeqFrom, "error", err)
		return l.gap(max(l.next, c.SeqFrom), c.SeqTo)
	}
	for i, ln := range lines {
		ln.Seq = c.SeqFrom + int64(i)
		if ln.Seq > c.SeqTo {
			break
		}
		if ln.Seq >= l.next && !l.line(ln) {
			return false
		}
	}
	if l.next <= c.SeqTo { // fewer lines than the manifest promised
		return l.gap(l.next, c.SeqTo)
	}
	return true
}

// storedLine is the ndjson line format of a chunk object (section 10).
type storedLine struct {
	T json.RawMessage `json:"t"`
	S string          `json:"s"`
	L string          `json:"l"`
}

func (l *logStream) readChunk(ctx context.Context, c store.LogChunk) ([]LogLine, error) {
	data, err := l.d.ds.GetLogData(ctx, c.ObjectKey)
	if err != nil {
		return nil, fmt.Errorf("reading chunk object: %w", err)
	}
	return decodeChunk(data)
}

// decodeChunk parses a chunk object, gzip-compressed or not.
func decodeChunk(data []byte) ([]LogLine, error) {
	var r io.Reader = bytes.NewReader(data)
	if len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("opening gzip chunk: %w", err)
		}
		defer func() { _ = zr.Close() }()
		r = zr
	}
	dec := json.NewDecoder(r)
	var out []LogLine
	for {
		var sl storedLine
		if err := dec.Decode(&sl); errors.Is(err, io.EOF) {
			return out, nil
		} else if err != nil {
			return out, fmt.Errorf("decoding chunk line %d: %w", len(out)+1, err)
		}
		out = append(out, LogLine{TS: parseStoredTime(sl.T), Stream: sl.S, Line: sl.L})
	}
}

// parseStoredTime accepts Unix nanoseconds or an RFC 3339 string.
func parseStoredTime(raw json.RawMessage) time.Time {
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return time.Unix(0, n).UTC()
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
