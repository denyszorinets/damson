package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store"
)

type sseEvent struct {
	ID   string
	Name string
	Data string
}

// sseStream is an open event stream read event by event.
type sseStream struct {
	t      *testing.T
	cancel context.CancelFunc
	r      *bufio.Reader
	heart  int
}

func openSSE(t *testing.T, srv *httptest.Server, path string, hdr map[string]string) *sseStream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	require.NoError(t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	return &sseStream{t: t, cancel: cancel, r: bufio.NewReader(resp.Body)}
}

// next returns the next event, skipping comments (counting heartbeats).
func (s *sseStream) next() (sseEvent, bool) {
	var ev sseEvent
	seen := false
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			return ev, false
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if seen {
				return ev, true
			}
		case strings.HasPrefix(line, ": heartbeat"):
			s.heart++
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "id: "):
			ev.ID, seen = strings.TrimPrefix(line, "id: "), true
		case strings.HasPrefix(line, "event: "):
			ev.Name, seen = strings.TrimPrefix(line, "event: "), true
		case strings.HasPrefix(line, "data: "):
			ev.Data, seen = strings.TrimPrefix(line, "data: "), true
		}
	}
}

// until collects events up to and including the first one named stop.
func (s *sseStream) until(stop string) []sseEvent {
	s.t.Helper()
	var out []sseEvent
	for {
		ev, ok := s.next()
		require.True(s.t, ok, "stream ended before %q; got %v", stop, out)
		out = append(out, ev)
		if ev.Name == stop {
			return out
		}
	}
}

func (s *sseStream) mustNext() sseEvent {
	s.t.Helper()
	ev, ok := s.next()
	require.True(s.t, ok, "stream ended")
	return ev
}

func names(evs []sseEvent) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Name + ":" + e.ID
	}
	return out
}

// startedRun triggers a run and claims, starts and returns its execution.
func (f *jrFixture) startedRun(t *testing.T) (runBody, store.Execution) {
	t.Helper()
	job := f.createJob(t, specDoc(fmt.Sprintf("j-%d", time.Now().UnixNano()), "", "ssh", `{"host":"h"}`))
	run := f.trigger(t, job.Job.ID, `{}`)
	got, err := f.st.ClaimExecutions(context.Background(), store.ClaimParams{
		AgentID: "a1", ControllerID: "c1", Pools: []string{"default"}, Limit: 1, LeaseTTL: time.Minute,
	}, t0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, run.Steps[0].Executions[0].ID, got[0].ID)
	require.NoError(t, f.st.MarkExecutionRunning(context.Background(), got[0].ID, got[0].AttemptID, t0))
	return run, got[0]
}

func (f *jrFixture) finish(t *testing.T, run runBody, e store.Execution, state store.ExecutionState) {
	t.Helper()
	ctx := context.Background()
	_, err := f.st.CompleteExecution(ctx, store.Completion{ExecutionID: e.ID, AttemptID: e.AttemptID, State: state}, t0)
	require.NoError(t, err)
	tree, err := f.st.GetRunTree(ctx, run.ID)
	require.NoError(t, err)
	_, err = f.st.UpdateStepInstance(ctx, store.StepUpdate{ID: tree.Steps[0].ID, ExpectedStatus: store.StepPending, Status: store.StepSucceeded}, t0)
	require.NoError(t, err)
	r, err := f.st.UpdateRunStatus(ctx, run.ID, tree.Version, store.RunRunning, t0)
	require.NoError(t, err)
	_, err = f.st.UpdateRunStatus(ctx, run.ID, r.Version, store.RunSucceeded, t0)
	require.NoError(t, err)
}

func TestRunEventsStream(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		srv := httptest.NewServer(f.h)
		defer srv.Close()
		run, exec := f.startedRun(t)

		s := openSSE(t, srv, "/api/v1/runs/"+run.ID+"/events", map[string]string{"Last-Event-ID": "7"})
		snap := s.mustNext()
		assert.Equal(t, "snapshot", snap.Name)
		assert.Equal(t, "8", snap.ID, "ids continue from Last-Event-ID")
		var det runBody
		require.NoError(t, json.Unmarshal([]byte(snap.Data), &det))
		assert.Equal(t, run.ID, det.ID)
		require.Len(t, det.Steps, 1)
		assert.Equal(t, "running", det.Steps[0].Executions[0].State)

		f.finish(t, run, exec, store.ExecSucceeded)
		evs := s.until("end")
		var steps []string
		for _, e := range evs {
			if e.Name == "step" {
				var se struct {
					Path    string `json:"path"`
					Lane    string `json:"lane"`
					Status  string `json:"status"`
					Attempt int    `json:"attempt"`
				}
				require.NoError(t, json.Unmarshal([]byte(e.Data), &se))
				assert.Equal(t, "a", se.Path)
				assert.Equal(t, "", se.Lane)
				assert.Equal(t, 1, se.Attempt)
				steps = append(steps, se.Status)
			}
		}
		require.NotEmpty(t, steps)
		assert.Equal(t, "succeeded", steps[len(steps)-1])
		assert.Contains(t, evs[len(evs)-1].Data, `"status":"succeeded"`)
		_, more := s.next()
		assert.False(t, more, "stream closes after end")

		// A finished run: snapshot, then end.
		s2 := openSSE(t, srv, "/api/v1/runs/"+run.ID+"/events", nil)
		assert.Equal(t, []string{"snapshot:1", "end:2"}, names(s2.until("end")))

		assert.Equal(t, http.StatusNotFound, f.call(t, http.MethodGet, "/api/v1/runs/nope/events", "").Code)
	})
}

// fakeTail replays lines pushed by the test.
type fakeTail struct {
	ch    chan api.LogLine
	after chan int64
}

func (f *fakeTail) Subscribe(_ context.Context, _ string, afterSeq int64) (<-chan api.LogLine, error) {
	f.after <- afterSeq
	return f.ch, nil
}

func (f *jrFixture) putChunk(t *testing.T, e store.Execution, from, to int64, lines ...string) {
	t.Helper()
	var sb strings.Builder
	for i, l := range lines {
		fmt.Fprintf(&sb, "{\"t\":%d,\"s\":\"out\",\"l\":%q}\n", t0.Add(time.Duration(i)*time.Second).UnixNano(), l)
	}
	key := fmt.Sprintf("logs/%s/%d-%d.ndjson", e.ID, from, to)
	ctx := context.Background()
	require.NoError(t, f.st.PutLogData(ctx, key, []byte(sb.String()), t0))
	_, err := f.st.InsertLogChunk(ctx, e.AttemptID, store.NewLogChunk{
		ExecutionID: e.ID, SeqFrom: from, SeqTo: to, Backend: "db", ObjectKey: key,
		Bytes: int64(sb.Len()), TsFirst: t0, TsLast: t0.Add(time.Second),
	}, t0)
	require.NoError(t, err)
}

func TestExecutionLogsStored(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		srv := httptest.NewServer(f.h)
		defer srv.Close()
		run, e := f.startedRun(t)
		f.putChunk(t, e, 1, 2, "one", "two")
		f.putChunk(t, e, 5, 6, "five", "six") // 3-4 were declared lost by the agent
		f.finish(t, run, e, store.ExecSucceeded)
		base := "/api/v1/executions/" + e.ID + "/logs"

		tests := []struct {
			name   string
			path   string
			hdr    map[string]string
			expect []string
		}{
			{name: "from the start", path: base,
				expect: []string{"log:1", "log:2", "gap:4", "log:5", "log:6", "end:"}},
			{name: "after_seq resumes mid chunk", path: base + "?after_seq=1",
				expect: []string{"log:2", "gap:4", "log:5", "log:6", "end:"}},
			{name: "after_seq past the gap", path: base + "?after_seq=4",
				expect: []string{"log:5", "log:6", "end:"}},
			{name: "Last-Event-ID wins over after_seq", path: base + "?after_seq=0", hdr: map[string]string{"Last-Event-ID": "5"},
				expect: []string{"log:6", "end:"}},
			{name: "after the last line", path: base + "?after_seq=6", expect: []string{"end:"}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				s := openSSE(t, srv, tt.path, tt.hdr)
				assert.Equal(t, tt.expect, names(s.until("end")))
			})
		}

		s := openSSE(t, srv, base, nil)
		evs := s.until("end")
		assert.JSONEq(t, fmt.Sprintf(`{"seq":1,"ts":%q,"stream":"out","line":"one"}`, t0.Format(time.RFC3339Nano)), evs[0].Data)
		assert.JSONEq(t, `{"seq_from":3,"seq_to":4}`, evs[2].Data)
		assert.JSONEq(t, `{"state":"succeeded","last_seq":6}`, evs[len(evs)-1].Data)

		assert.Equal(t, http.StatusNotFound, f.call(t, http.MethodGet, "/api/v1/executions/nope/logs", "").Code)
		assert.Equal(t, http.StatusUnprocessableEntity, f.call(t, http.MethodGet, base+"?after_seq=-1", "").Code)
	})
}

func TestExecutionLogsMissingObjectIsAGap(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		srv := httptest.NewServer(f.h)
		defer srv.Close()
		run, e := f.startedRun(t)
		f.putChunk(t, e, 1, 2, "one", "two")
		f.putChunk(t, e, 3, 4, "three", "four")
		_, err := st.DeleteLogDataPrefix(context.Background(), fmt.Sprintf("logs/%s/3-4", e.ID))
		require.NoError(t, err)
		f.finish(t, run, e, store.ExecSucceeded)

		s := openSSE(t, srv, "/api/v1/executions/"+e.ID+"/logs", nil)
		evs := s.until("end")
		assert.Equal(t, []string{"log:1", "log:2", "gap:4", "end:"}, names(evs))
		assert.JSONEq(t, `{"seq_from":3,"seq_to":4}`, evs[2].Data)
	})
}

// TestExecutionLogsPollingTail covers a running execution without a LogTail:
// the stream keeps polling the store, sends new chunks, heartbeats while idle
// and ends once the execution is terminal.
func TestExecutionLogsPollingTail(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		srv := httptest.NewServer(f.h)
		defer srv.Close()
		run, e := f.startedRun(t)
		f.putChunk(t, e, 1, 1, "first")

		s := openSSE(t, srv, "/api/v1/executions/"+e.ID+"/logs", nil)
		assert.Equal(t, "log", s.mustNext().Name)
		f.putChunk(t, e, 2, 3, "second", "third")
		assert.Equal(t, "2", s.mustNext().ID)
		assert.Equal(t, "3", s.mustNext().ID)
		time.Sleep(150 * time.Millisecond) // idle: several heartbeat intervals
		f.finish(t, run, e, store.ExecSucceeded)
		assert.Equal(t, "end", s.mustNext().Name)
		assert.Positive(t, s.heart, "idle stream sends heartbeats")
	})
}

func TestExecutionLogsLiveTail(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		tail := &fakeTail{ch: make(chan api.LogLine, 8), after: make(chan int64, 1)}
		f := newJR(t, st, tail)
		srv := httptest.NewServer(f.h)
		defer srv.Close()
		run, e := f.startedRun(t)
		f.putChunk(t, e, 1, 2, "one", "two")

		s := openSSE(t, srv, "/api/v1/executions/"+e.ID+"/logs?after_seq=1", nil)
		assert.Equal(t, int64(1), <-tail.after, "the tail resumes after the client's seq")
		assert.Equal(t, "2", s.mustNext().ID)

		tail.ch <- api.LogLine{Seq: 2, Stream: "out", Line: "dup of a stored line"} // skipped
		tail.ch <- api.LogLine{Seq: 3, TS: t0, Stream: "err", Line: "live three"}
		tail.ch <- api.LogLine{Seq: 6, TS: t0, Stream: "out", Line: "live six"} // 4-5 never arrive
		ev := s.mustNext()
		assert.Equal(t, "3", ev.ID)
		assert.Contains(t, ev.Data, `"stream":"err"`)
		assert.Contains(t, ev.Data, "live three")
		gap := s.mustNext()
		assert.Equal(t, "gap", gap.Name)
		assert.JSONEq(t, `{"seq_from":4,"seq_to":5}`, gap.Data)
		assert.Equal(t, "6", s.mustNext().ID)

		close(tail.ch)
		f.finish(t, run, e, store.ExecSucceeded)
		assert.Equal(t, "end", s.mustNext().Name)
	})
}

// TestSSEClientDisconnect proves a stream goroutine ends when the client goes
// away: Close blocks until every handler returned, and TestMain's goleak
// check fails the run on any leftover goroutine.
func TestSSEClientDisconnect(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		tail := &fakeTail{ch: make(chan api.LogLine), after: make(chan int64, 1)}
		f := newJR(t, st, tail)
		srv := httptest.NewServer(f.h)
		run, e := f.startedRun(t)

		logs := openSSE(t, srv, "/api/v1/executions/"+e.ID+"/logs", nil)
		events := openSSE(t, srv, "/api/v1/runs/"+run.ID+"/events", nil)
		assert.Equal(t, "snapshot", events.mustNext().Name)
		<-tail.after
		logs.cancel()
		events.cancel()

		done := make(chan struct{})
		go func() { srv.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("handlers did not return after the clients disconnected")
		}
	})
}

// TestExecutionLogsNeverEchoRequestData: log lines are served as stored (the
// agent masks secrets before they leave it) and the controller never writes
// them to its own log.
func TestExecutionLogsNeverLogLines(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		srv := httptest.NewServer(f.h)
		defer srv.Close()
		run, e := f.startedRun(t)
		f.putChunk(t, e, 1, 1, "password=***")
		f.finish(t, run, e, store.ExecSucceeded)
		s := openSSE(t, srv, "/api/v1/executions/"+e.ID+"/logs", nil)
		evs := s.until("end")
		assert.Contains(t, evs[0].Data, "password=***")
		assert.NotContains(t, f.logs.String(), "password=")
	})
}
