package agent

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// finishedStep is a completed step whose step.complete the controller has not
// acknowledged. It lives in memory only; it is listed in agent.hello and
// resent until the controller acknowledges it or calls it stale.
type finishedStep struct {
	msg      protocol.StepComplete
	at       time.Time
	seq      uint64    // finish order, for dropping the oldest
	acked    bool      // the controller acknowledged the result; unsent logs remain
	failures int       // consecutive failed deliveries (not transport errors)
	retryAt  time.Time // do not retry before this instant
}

const (
	// maxFinished bounds the unacknowledged results kept in memory; the
	// oldest goes first.
	maxFinished = 1024
	// maxFinishedAge is how long an unacknowledged result is kept. The
	// controller's lease reaper has long marked the attempt lost by then.
	maxFinishedAge = 6 * time.Hour
	// maxResultBackoff caps the per-result retry delay after a failed delivery.
	maxResultBackoff = 30 * time.Second
	// maxErrorBytes caps an error message in step.complete.
	maxErrorBytes = 8 << 10
)

// buildComplete maps the plugin outcome to a step.complete message. Every
// text and output value passes the masker. The status mapping:
//
//	panic                        failed, code internal
//	Validate failed              failed, code validation
//	failure while ctx timed out  timed_out, code timeout
//	failure while ctx cancelled  canceled, code cancelled
//	Result.Error or error return failed, code = SDK error kind
//	otherwise                    succeeded
func (e *Executor) buildComplete(r *run, out pluginOutcome, m *masker, pipe *logPipe, started time.Time) protocol.StepComplete {
	msg := protocol.StepComplete{AttemptID: r.attempt, StartedAtNS: started.UnixNano()}
	serr := failureOf(out)
	ctxErr := r.execCtx.Err()
	switch {
	case out.abandoned || (serr != nil && !out.panicked && !out.validation && ctxErr != nil):
		msg.Status, msg.Error = ctxStatus(ctxErr)
	case serr != nil:
		msg.Status = protocol.StatusFailed
		msg.Error = &protocol.StepError{Code: string(serr.Kind), Message: truncate(m.Mask(serr.Message), maxErrorBytes), Retryable: serr.Kind == pluginsdk.KindConnection}
	default:
		msg.Status = protocol.StatusSucceeded
	}
	if !out.abandoned && !out.panicked {
		outputs, redacted, err := sanitizeOutputs(out.result.Outputs, m)
		switch {
		case err != nil && msg.Status == protocol.StatusSucceeded:
			msg.Status = protocol.StatusFailed
			msg.Error = &protocol.StepError{Code: string(pluginsdk.KindInternal), Message: err.Error()}
		case err == nil:
			msg.Outputs = outputs
		}
		if len(redacted) > 0 {
			e.log.Warn("step output contained a secret and was redacted", "attempt_id", r.attempt, "paths", redacted)
			pipe.system("warning: step output contained a secret and was redacted")
		}
	}
	msg.FinishedAtNS = e.cfg.Now().UnixNano()
	return msg
}

// truncate cuts s to at most n bytes on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "...(truncated)"
}

// failureOf returns the step failure in out, or nil on success.
func failureOf(out pluginOutcome) *pluginsdk.StepError {
	switch {
	case out.panicked:
		return pluginsdk.NewError(pluginsdk.KindInternal, "plugin panicked", nil)
	case out.err != nil:
		return pluginsdk.AsStepError(out.err)
	case out.result.Error != nil:
		return out.result.Error
	}
	return nil
}

func ctxStatus(ctxErr error) (string, *protocol.StepError) {
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return protocol.StatusTimedOut, &protocol.StepError{Code: string(pluginsdk.KindTimeout), Message: "step timed out"}
	}
	return protocol.StatusCanceled, &protocol.StepError{Code: string(pluginsdk.KindCancelled), Message: "step cancelled"}
}

// finish moves the step from running to finished and wakes the delivery loop.
// The finished set is bounded: past maxFinished the oldest result is dropped
// (and remembered as completed so a replayed execute is still refused).
func (e *Executor) finish(r *run, msg protocol.StepComplete) {
	now := e.cfg.Now()
	e.mu.Lock()
	delete(e.runs, r.attempt)
	e.finishSeq++
	e.done[r.attempt] = &finishedStep{msg: msg, at: now, seq: e.finishSeq}
	dropped := e.pruneDoneLocked(now)
	e.mu.Unlock()
	for _, id := range dropped {
		e.hub.forget(id)
	}
	e.kickComplete()
}

// pruneDoneLocked drops finished results that are too old or exceed the cap
// and returns their attempt ids. Dropped ids go into the completed set.
func (e *Executor) pruneDoneLocked(now time.Time) []string {
	var drop []string
	for id, f := range e.done {
		if now.Sub(f.at) > maxFinishedAge {
			drop = append(drop, id)
		}
	}
	for _, id := range drop {
		delete(e.done, id)
	}
	for len(e.done) > maxFinished {
		var oldest string
		var seq uint64
		for id, f := range e.done {
			if oldest == "" || f.seq < seq {
				oldest, seq = id, f.seq
			}
		}
		delete(e.done, oldest)
		drop = append(drop, oldest)
	}
	for _, id := range drop {
		e.completed.add(id, now)
		e.log.Warn("dropped an unacknowledged step result (too old or too many pending)", "attempt_id", id)
	}
	return drop
}

func (e *Executor) kickComplete() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// completeLoop delivers finished steps whenever one appears, a connection is
// attached, or the retry interval passes.
func (e *Executor) completeLoop(ctx context.Context) {
	t := e.cfg.Clock.ticker(e.cfg.CompleteRetry)
	defer t.Stop()
	for {
		e.deliverFinished(ctx)
		select {
		case <-e.wake:
		case <-t.C():
		case <-ctx.Done():
			return
		}
	}
}

// deliverFinished sends step.complete for every finished step. Logs go first
// so the controller has them before the result. A transport error (or no
// connection) ends the round: nothing else would get through. An error answer
// or a malformed acknowledgement concerns that result alone: it backs off on
// its own and the next result is tried. An acknowledgement, stale or not,
// ends the retries, but the attempt's logs are kept until none is unsent.
func (e *Executor) deliverFinished(ctx context.Context) {
	if e.link.current() == nil {
		return
	}
	e.hub.pump(ctx)
	for _, f := range e.pendingResults() {
		if f.acked {
			e.settle(f.msg.AttemptID)
			continue
		}
		if e.cfg.Now().Before(f.retryAt) {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, completeCallTimeout)
		var ack protocol.StepCompleteAck
		err := e.link.call(cctx, protocol.MethodStepComplete, f.msg, &ack)
		cancel()
		var rpcErr *protocol.RPCError
		switch {
		case errors.As(err, &rpcErr):
			e.failResult(f.msg.AttemptID, "controller answered with an error", rpcErr.Code)
		case err != nil:
			e.log.Debug("step.complete not delivered; will retry", "attempt_id", f.msg.AttemptID, "error", err)
			return
		case !ack.Acknowledged && !ack.Stale:
			e.failResult(f.msg.AttemptID, "acknowledgement neither acknowledged nor stale", 0)
		default:
			e.log.Info("step result acknowledged", "attempt_id", f.msg.AttemptID, "status", f.msg.Status, "stale", ack.Stale)
			e.markAcked(f.msg.AttemptID)
			e.settle(f.msg.AttemptID)
		}
	}
}

// failResult schedules the next try of one result with exponential backoff
// and jitter, capped at maxResultBackoff.
func (e *Executor) failResult(attempt, reason string, code int) {
	e.mu.Lock()
	f := e.done[attempt]
	var n int
	if f != nil {
		f.failures++
		n = f.failures
		f.retryAt = e.cfg.Now().Add(BackoffDelay(n-1, e.cfg.CompleteRetry, maxResultBackoff, e.cfg.Rand()))
	}
	e.mu.Unlock()
	e.log.Warn("step.complete failed; backing off", "attempt_id", attempt, "reason", reason, "code", code, "failures", n)
}

func (e *Executor) markAcked(attempt string) {
	e.mu.Lock()
	if f := e.done[attempt]; f != nil {
		f.acked = true
	}
	e.mu.Unlock()
}

// settle forgets an acknowledged result and its logs, unless logs are still
// unsent: then the entry stays and a later round sends them first.
func (e *Executor) settle(attempt string) {
	e.hub.pump(e.base)
	if e.hub.hasUnsent(attempt) {
		e.log.Debug("keeping logs of an acknowledged step until they are sent", "attempt_id", attempt)
		return
	}
	e.drop(attempt)
}

// resetRetry lets every result retry at once; called on a new connection.
func (e *Executor) resetRetry() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, f := range e.done {
		f.retryAt = time.Time{}
	}
}

// pendingResults snapshots the finished steps, oldest first.
func (e *Executor) pendingResults() []finishedStep {
	e.mu.Lock()
	defer e.mu.Unlock()
	list := make([]finishedStep, 0, len(e.done))
	for _, f := range e.done {
		list = append(list, *f)
	}
	slices.SortFunc(list, func(a, b finishedStep) int {
		return cmp.Compare(a.seq, b.seq)
	})
	return list
}

// drop forgets a finished step and its buffered logs. The attempt is
// remembered as completed so a late step.execute for it is refused.
func (e *Executor) drop(attempt string) {
	e.mu.Lock()
	delete(e.done, attempt)
	e.completed.add(attempt, e.cfg.Now())
	e.mu.Unlock()
	e.hub.forget(attempt)
}

// States lists running and finished-but-unacknowledged attempts for agent.hello.
func (e *Executor) States() []protocol.ExecutionState {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]protocol.ExecutionState, 0, len(e.runs)+len(e.done))
	for id := range e.runs {
		out = append(out, protocol.ExecutionState{AttemptID: id, State: protocol.ExecRunning})
	}
	for id, f := range e.done {
		if !f.acked { // the controller has the result of an acked one
			out = append(out, protocol.ExecutionState{AttemptID: id, State: protocol.ExecFinished})
		}
	}
	slices.SortFunc(out, func(a, b protocol.ExecutionState) int {
		return strings.Compare(a.AttemptID, b.AttemptID)
	})
	return out
}

// Reconcile applies the controller's decisions from the hello response.
// cancel stops a running attempt and discards a finished one (its result is
// stale); continue and absent attempts are left alone.
func (e *Executor) Reconcile(recs []protocol.Reconciliation) {
	for _, rec := range recs {
		if rec.Action != protocol.ActionCancel {
			continue
		}
		e.mu.Lock()
		r := e.runs[rec.AttemptID]
		_, finished := e.done[rec.AttemptID]
		e.mu.Unlock()
		switch {
		case r != nil:
			e.log.Info("controller cancelled attempt on reconnect", "attempt_id", rec.AttemptID)
			r.requestCancel(e.cfg.CancelGrace)
		case finished:
			e.log.Info("controller discarded finished attempt on reconnect", "attempt_id", rec.AttemptID)
			e.drop(rec.AttemptID)
		}
	}
}

// Attach makes peer the connection for logs, results and host keys, resends
// unacknowledged logs and wakes the delivery loop. Call it after the hello
// response was reconciled.
func (e *Executor) Attach(peer *protocol.Peer, limits protocol.Limits) {
	if limits.LogBatchBytes > 0 {
		e.hub.batchBytes.Store(int64(limits.LogBatchBytes))
	}
	e.link.set(peer)
	e.resetRetry()
	e.hub.resetSent()
	e.spawn(func() { e.hub.pump(e.base) })
	e.kickComplete()
}

// Detach forgets peer when it is still the current connection.
func (e *Executor) Detach(peer *protocol.Peer) { e.link.clearIf(peer) }
