package agent

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// finishedStep is a completed step whose step.complete the controller has not
// acknowledged. It lives in memory only; it is listed in agent.hello and
// resent until the controller acknowledges it or calls it stale.
type finishedStep struct {
	msg protocol.StepComplete
}

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
		msg.Error = &protocol.StepError{Code: string(serr.Kind), Message: m.Mask(serr.Message), Retryable: serr.Kind == pluginsdk.KindConnection}
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
func (e *Executor) finish(r *run, msg protocol.StepComplete) {
	e.mu.Lock()
	delete(e.runs, r.attempt)
	e.done[r.attempt] = &finishedStep{msg: msg}
	e.mu.Unlock()
	e.kickComplete()
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
// so the controller has them before the result. A send error leaves the step
// for the next round; an acknowledgement, stale or not, ends the retries.
func (e *Executor) deliverFinished(ctx context.Context) {
	if e.link.current() == nil {
		return
	}
	for _, msg := range e.pendingResults() {
		e.hub.pump(ctx)
		cctx, cancel := context.WithTimeout(ctx, completeCallTimeout)
		var ack protocol.StepCompleteAck
		err := e.link.call(cctx, protocol.MethodStepComplete, msg, &ack)
		cancel()
		if err != nil {
			e.log.Debug("step.complete not delivered; will retry", "attempt_id", msg.AttemptID, "error", err)
			return
		}
		if ack.Acknowledged || ack.Stale {
			e.log.Info("step result acknowledged", "attempt_id", msg.AttemptID, "status", msg.Status, "stale", ack.Stale)
			e.drop(msg.AttemptID)
		}
	}
}

func (e *Executor) pendingResults() []protocol.StepComplete {
	e.mu.Lock()
	defer e.mu.Unlock()
	list := make([]protocol.StepComplete, 0, len(e.done))
	for _, f := range e.done {
		list = append(list, f.msg)
	}
	slices.SortFunc(list, func(a, b protocol.StepComplete) int {
		return cmp.Compare(a.FinishedAtNS, b.FinishedAtNS)
	})
	return list
}

// drop forgets a finished step and its buffered logs.
func (e *Executor) drop(attempt string) {
	e.mu.Lock()
	delete(e.done, attempt)
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
	for id := range e.done {
		out = append(out, protocol.ExecutionState{AttemptID: id, State: protocol.ExecFinished})
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
	e.hub.resetSent()
	e.spawn(func() { e.hub.pump(e.base) })
	e.kickComplete()
}

// Detach forgets peer when it is still the current connection.
func (e *Executor) Detach(peer *protocol.Peer) { e.link.clearIf(peer) }
