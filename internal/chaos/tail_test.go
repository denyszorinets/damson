//go:build chaos

package chaos

import (
	"bufio"
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sseLogs reads an execution's log stream through controller idx.
type sseLogs struct {
	t      *testing.T
	events chan sseEvent
	cancel context.CancelFunc
}

type sseEvent struct{ name, id string }

func (c *Cluster) openLogs(idx int, executionID string) *sseLogs {
	c.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c.t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ctls[idx].srv.URL+"/api/v1/executions/"+executionID+"/logs", nil)
	if err != nil {
		c.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	s := &sseLogs{t: c.t, events: make(chan sseEvent, 256), cancel: cancel}
	go func() {
		defer func() { _ = resp.Body.Close(); close(s.events) }()
		var ev sseEvent
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				ev.id = strings.TrimPrefix(line, "id: ")
			case line == "": // end of an event
				if ev.name != "" || ev.id != "" {
					s.events <- ev
				}
				ev = sseEvent{}
			}
		}
	}()
	return s
}

// next returns the next log or end event, skipping comments and heartbeats.
func (s *sseLogs) next(within time.Duration) (sseEvent, bool) {
	s.t.Helper()
	select {
	case ev, ok := <-s.events:
		return ev, ok
	case <-time.After(within):
		s.t.Fatal("timed out waiting for a log event")
		return sseEvent{}, false
	}
}

func (c *Cluster) logChunks(executionID string) int {
	c.t.Helper()
	var n int
	if err := c.read.Pool().QueryRow(context.Background(), "SELECT count(*) FROM log_chunks WHERE execution_id = $1", executionID).Scan(&n); err != nil {
		c.t.Fatal(err)
	}
	return n
}

// B5 (#89): the log of an execution running on controller A is tailed through
// controller B. The lines arrive over the internal endpoint before any chunk
// is stored; when A dies mid-tail the agent is adopted by B, and the stream
// carries on from the store: every sequence number exactly once, then the end.
func TestLogTailThroughOtherControllerSurvivesOwnerCrash(t *testing.T) {
	c := newCluster(t, 2)
	gp := newGatePlugin()
	c.addAgent("ag1", 0, gp)
	runID := c.trigger(0)
	ex := c.runningOn(runID, "ctl-A")

	logs := c.openLogs(1, ex.ID)
	var seen []int64
	for len(seen) < 3 {
		ev, ok := logs.next(waitTimeout)
		if !ok {
			t.Fatalf("stream ended after %v", seen)
		}
		if ev.id == "" {
			continue
		}
		n, err := strconv.ParseInt(ev.id, 10, 64)
		if err != nil {
			t.Fatalf("event id %q", ev.id)
		}
		seen = append(seen, n)
	}
	if n := c.logChunks(ex.ID); n != 0 {
		t.Fatalf("%d chunks stored before the tail delivered: the lines did not come over the proxy", n)
	}

	// A dies: its database connections go away and its agent is cut off, then
	// the agent reconnects to B and is adopted.
	c.lb.route("ag1", "")
	c.lb.sever("ag1")
	if err := c.ctls[0].st.Close(); err != nil {
		t.Fatal(err)
	}
	c.lb.route("ag1", "ctl-B")
	gp.open()
	c.waitSucceeded(runID)

	for {
		ev, ok := logs.next(waitTimeout)
		if !ok {
			t.Fatal("stream closed without an end event")
		}
		if ev.name == "end" {
			break
		}
		if ev.name == "gap" {
			t.Fatalf("gap in the log stream after %v", seen)
		}
		if ev.id == "" {
			continue
		}
		n, _ := strconv.ParseInt(ev.id, 10, 64)
		if n != seen[len(seen)-1]+1 {
			t.Fatalf("seq %d after %d: duplicate or hole (%v)", n, seen[len(seen)-1], seen)
		}
		seen = append(seen, n)
	}
	if len(seen) < 3 {
		t.Fatalf("saw %v", seen)
	}
}
