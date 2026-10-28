package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/store"
)

// RunNotifier lets the orchestrator wake run event streams as soon as a run
// changes, instead of waiting for the next poll. The stream still polls, so a
// missed notification only delays an event. Subscribe returns a channel that
// receives a value (coalesced) per change, and a release function.
type RunNotifier interface {
	Subscribe(ctx context.Context, runID string) (<-chan struct{}, func())
}

// RunStatusEvent is the payload of a "run" event.
type RunStatusEvent struct {
	Status          string `json:"status"`
	CancelRequested bool   `json:"cancel_requested"`
}

// StepEvent is the payload of a "step" event: the state of one step instance
// and its latest attempt.
type StepEvent struct {
	Path        string `json:"path"`
	Lane        string `json:"lane" doc:"for_each lane of the instance; always empty before M3."`
	Status      string `json:"status"`
	Attempt     int    `json:"attempt" doc:"Latest attempt number, 0 before the first execution."`
	ExecutionID string `json:"execution_id,omitempty"`
	State       string `json:"state,omitempty" doc:"State of the latest execution."`
}

type runEventsInput struct {
	ID          string `path:"id" maxLength:"128"`
	LastEventID string `header:"Last-Event-ID" doc:"Resume marker sent by EventSource. The server cannot replay missed events, so it always starts with a fresh snapshot and continues the event ids from this value."`
}

func registerRunEvents(api huma.API, d deps) {
	huma.Register(api, huma.Operation{
		OperationID: "stream-run-events", Method: http.MethodGet, Path: "/runs/{id}/events",
		Summary: "Server-sent events of a run", Tags: []string{"runs"}, Errors: []int{http.StatusNotFound, http.StatusServiceUnavailable},
		Description: "Events: snapshot (the full run with its step tree), then run and step as they change, then end once the run is finished. Every event has a numeric id. At most StreamConfig.MaxStreams streams are open at once; beyond that the answer is 503.",
		Responses:   sseOperationResponses("An event stream."),
	}, func(ctx context.Context, in *runEventsInput) (*huma.StreamResponse, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		// The stamp is read before the tree, so a change in between is
		// caught by the first poll.
		stamp, err := d.ds.GetRunStamp(ctx, in.ID)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		tree, err := d.ds.GetRunTree(ctx, in.ID)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		ver, err := d.ds.GetJobVersion(ctx, tree.JobVersionID)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		release, err := d.acquireStream()
		if err != nil {
			return nil, apiError(ctx, err)
		}
		last, _ := parseEventID(in.LastEventID)
		return sseResponse(d, release, func(ctx context.Context, s *sseWriter) {
			streamRun(ctx, d, s, tree, stamp, toRunDetail(tree, ver.Version), last)
		}), nil
	})
}

// runFingerprint is the comparable state of a run tree.
type runFingerprint struct {
	run   RunStatusEvent
	steps map[string]StepEvent
}

func fingerprint(t store.RunTree) runFingerprint {
	fp := runFingerprint{
		run:   RunStatusEvent{Status: string(t.Status), CancelRequested: t.CancelRequestedAt != nil},
		steps: make(map[string]StepEvent, len(t.Steps)),
	}
	for _, n := range t.Steps {
		ev := StepEvent{Path: n.Path, Status: string(n.Status)}
		if k := len(n.Executions); k > 0 {
			e := n.Executions[k-1]
			ev.Attempt, ev.ExecutionID, ev.State = e.AttemptNo, e.ID, string(e.State)
		}
		fp.steps[n.Path] = ev
	}
	return fp
}

// streamRun sends the snapshot, then polls: a cheap stamp first, and the full
// tree only when the stamp moved.
func streamRun(ctx context.Context, d deps, s *sseWriter, tree store.RunTree, stamp store.RunStamp, snap RunDetail, last int64) {
	id := last
	send := func(name string, data any) bool {
		id++
		return s.event(strconv.FormatInt(id, 10), name, data) == nil
	}
	if !send("snapshot", snap) {
		return
	}
	prev := fingerprint(tree)
	if terminalRun(prev.run.Status) {
		send("end", prev.run)
		return
	}
	var wake <-chan struct{}
	if d.notify != nil {
		ch, release := d.notify.Subscribe(ctx, snap.ID)
		defer release()
		wake = ch
	}
	beat := time.NewTicker(d.stream.Heartbeat)
	defer beat.Stop()
	for {
		timer := time.NewTimer(jitter(d.stream.PollInterval))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-beat.C:
			timer.Stop()
			if s.comment("heartbeat") != nil {
				return
			}
			continue
		case <-timer.C:
		case <-wake:
			timer.Stop()
		}
		changed, err := pollRun(ctx, d, snap.ID, &stamp)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, store.ErrNotFound) {
				return
			}
			loggerFromCtx(ctx).Warn("run events: reading run", "run_id", snap.ID, "error", err)
			continue
		}
		if changed == nil {
			continue // nothing changed
		}
		fp := fingerprint(*changed)
		if !emitChanges(send, *changed, prev, fp) {
			return
		}
		prev = fp
		if terminalRun(fp.run.Status) {
			send("end", fp.run)
			return
		}
	}
}

// pollRun reads the stamp and, if it moved, the tree (nil when unchanged).
func pollRun(ctx context.Context, d deps, runID string, last *store.RunStamp) (*store.RunTree, error) {
	st, err := d.ds.GetRunStamp(ctx, runID)
	if err != nil {
		return nil, err //nolint:wrapcheck // classified by the caller
	}
	if st == *last {
		return nil, nil
	}
	tree, err := d.ds.GetRunTree(ctx, runID)
	if err != nil {
		return nil, err //nolint:wrapcheck // classified by the caller
	}
	*last = st
	return &tree, nil
}

// emitChanges sends a run event and one step event per changed step, in step
// order. It reports false when the client is gone.
func emitChanges(send func(string, any) bool, tree store.RunTree, prev, cur runFingerprint) bool {
	if cur.run != prev.run && !send("run", cur.run) {
		return false
	}
	for _, n := range tree.Steps {
		if ev := cur.steps[n.Path]; ev != prev.steps[n.Path] && !send("step", ev) {
			return false
		}
	}
	return true
}

func terminalRun(status string) bool { return store.RunStatus(status).Terminal() }
