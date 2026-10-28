package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/denyszorinets/damson/examples/plugins/hello"
	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

func TestExecuteHelloPlugin(t *testing.T) {
	ctl := newFakeCtl(t)
	ctl.autoAck.Store(true)
	startAgent(t, ctl, hello.Plugins())

	req := protocol.StepExecute{
		AttemptID: "a1", ExecutionID: "e1", PluginName: "hello", PluginVersion: "0.1.0",
		Input: map[string]any{"name": "damson"},
	}
	ctl.mustExecute(req)
	c := completeOf(t, ctl, "a1")
	if c.Status != protocol.StatusSucceeded || c.Error != nil {
		t.Fatalf("complete = %+v", c)
	}
	if c.Outputs["greeting"] != "Hello, damson!" {
		t.Errorf("outputs = %v", c.Outputs)
	}
	if c.StartedAtNS == 0 || c.FinishedAtNS < c.StartedAtNS {
		t.Errorf("timings = %d..%d", c.StartedAtNS, c.FinishedAtNS)
	}
	lines := ctl.lines("a1")
	if len(lines) != 1 || lines[1].Line != "Hello, damson!" || lines[1].Stream != pluginsdk.Stdout {
		t.Errorf("lines = %+v", lines)
	}
	eventually(t, "state cleared after ack", func() bool {
		hb, ok := ctl.lastBeat()
		return ok && len(hb.RunningAttempts) == 0 && hb.FreeSlots == 4
	})
}

func TestLogBatchesAreOrderedAndContiguous(t *testing.T) {
	const lines = 300
	ctl := newFakeCtl(t)
	ctl.autoAck.Store(true)
	p := plug(func(_ context.Context, _ pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
		for i := range lines {
			emit.Log(pluginsdk.Stdout, fmt.Sprintf("line %03d %s", i, strings.Repeat("x", 200)))
		}
		return pluginsdk.Result{}, nil
	})
	startAgent(t, ctl, []pluginsdk.Plugin{p}, func(c *Config) { c.LogBatchBytes = 16 << 10 })
	ctl.mustExecute(stepReq("a1"))
	completeOf(t, ctl, "a1")

	appends := ctl.logsOf("a1")
	if len(appends) < 3 {
		t.Fatalf("got %d batches, want several for a 16 KiB threshold", len(appends))
	}
	next := uint64(1)
	for _, la := range appends {
		if la.SeqFrom != next {
			t.Fatalf("batch starts at seq %d, want %d (contiguous, ordered)", la.SeqFrom, next)
		}
		next += uint64(len(la.Entries))
	}
	if next != lines+1 {
		t.Errorf("total entries = %d, want %d", next-1, lines)
	}
	for i := range uint64(lines) {
		if !strings.HasPrefix(ctl.lines("a1")[i+1].Line, fmt.Sprintf("line %03d", i)) {
			t.Fatalf("entry %d out of order", i+1)
		}
	}
}

func TestLongLinesAreSplit(t *testing.T) {
	ctl := newFakeCtl(t)
	ctl.autoAck.Store(true)
	long := strings.Repeat("é", maxLineBytes) // 2 bytes per rune: splits must not cut a rune
	p := plug(func(_ context.Context, _ pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
		emit.Log(pluginsdk.Stderr, long)
		return pluginsdk.Result{}, nil
	})
	startAgent(t, ctl, []pluginsdk.Plugin{p})
	ctl.mustExecute(stepReq("a1"))
	completeOf(t, ctl, "a1")
	var got strings.Builder
	lines := ctl.lines("a1")
	for i := uint64(1); i <= uint64(len(lines)); i++ {
		if len(lines[i].Line) > maxLineBytes {
			t.Errorf("entry %d has %d bytes", i, len(lines[i].Line))
		}
		got.WriteString(lines[i].Line)
	}
	if len(lines) < 2 || got.String() != long {
		t.Errorf("entries = %d, reassembled equal = %v", len(lines), got.String() == long)
	}
}

func TestRejects(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T, ctl *fakeCtl, b *blocker)
		req    protocol.StepExecute
		reason string
	}{
		{
			name:   "unknown plugin",
			req:    protocol.StepExecute{AttemptID: "a1", PluginName: "nope", PluginVersion: "1.0.0"},
			reason: "unknown plugin nope@1.0.0",
		},
		{
			name:   "unknown version",
			req:    protocol.StepExecute{AttemptID: "a1", PluginName: "demo", PluginVersion: "9.9.9"},
			reason: "unknown plugin demo@9.9.9",
		},
		{
			name: "no free slot",
			setup: func(t *testing.T, ctl *fakeCtl, b *blocker) {
				ctl.mustExecute(stepReq("busy"))
				b.waitStarted(t)
			},
			req:    stepReq("a1"),
			reason: "no free slot",
		},
		{
			name: "draining",
			setup: func(t *testing.T, ctl *fakeCtl, _ *blocker) {
				ctl.notify(protocol.MethodAgentDrain, protocol.Drain{Reason: "test"})
				eventually(t, "drain applied", func() bool {
					hb, ok := ctl.lastBeat()
					return ok && hb.FreeSlots == 0
				})
			},
			req:    stepReq("a1"),
			reason: "draining",
		},
		{
			name: "cancelled before start",
			setup: func(t *testing.T, ctl *fakeCtl, _ *blocker) {
				if ctl.cancel("a1", 100).Known {
					t.Error("cancel of an unknown attempt reported known")
				}
			},
			req:    stepReq("a1"),
			reason: "cancelled before start",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctl := newFakeCtl(t)
			b := newBlocker()
			startAgent(t, ctl, []pluginsdk.Plugin{plug(b.run)}, func(c *Config) { c.Slots = 1 })
			if tt.setup != nil {
				tt.setup(t, ctl, b)
			}
			res, err := ctl.execute(tt.req)
			if err != nil {
				t.Fatal(err)
			}
			if res.Accepted || res.Reason != tt.reason {
				t.Errorf("result = %+v, want rejected %q", res, tt.reason)
			}
			close(b.release)
		})
	}
}

func TestDuplicateExecuteIsIdempotent(t *testing.T) {
	ctl := newFakeCtl(t)
	b := newBlocker()
	startAgent(t, ctl, []pluginsdk.Plugin{plug(b.run)})
	ctl.mustExecute(stepReq("a1"))
	b.waitStarted(t)
	ctl.mustExecute(stepReq("a1")) // a retried dispatch: accepted, not run twice
	close(b.release)
	completeOf(t, ctl, "a1")
	select {
	case <-b.started:
		t.Error("plugin ran twice")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestNeverExceedsSlots(t *testing.T) {
	const slots, tries = 3, 12
	ctl := newFakeCtl(t)
	b := newBlocker()
	startAgent(t, ctl, []pluginsdk.Plugin{plug(b.run)}, func(c *Config) { c.Slots = slots })

	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for i := range tries {
		wg.Go(func() {
			res, err := ctl.execute(stepReq(fmt.Sprintf("a%d", i)))
			if err != nil {
				t.Error(err)
			}
			if res.Accepted {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if accepted != slots {
		t.Errorf("accepted %d, want exactly %d", accepted, slots)
	}
	eventually(t, "heartbeat shows full house", func() bool {
		hb, ok := ctl.lastBeat()
		return ok && hb.FreeSlots == 0 && len(hb.RunningAttempts) == slots && slices.IsSorted(hb.RunningAttempts)
	})
	close(b.release)
	eventually(t, "slots free again", func() bool {
		hb, ok := ctl.lastBeat()
		return ok && hb.FreeSlots == slots && len(hb.RunningAttempts) == 0
	})
}

func TestCancelMidRun(t *testing.T) {
	ctl := newFakeCtl(t)
	b := newBlocker()
	startAgent(t, ctl, []pluginsdk.Plugin{plug(b.run)})
	ctl.mustExecute(stepReq("a1"))
	b.waitStarted(t)
	if !ctl.cancel("a1", 1000).Known {
		t.Error("cancel of a running attempt reported unknown")
	}
	c := completeOf(t, ctl, "a1")
	if c.Status != protocol.StatusCanceled || c.Error == nil || c.Error.Code != "cancelled" {
		t.Errorf("complete = %+v", c)
	}
}

func TestCancelAbandonsPluginAfterGrace(t *testing.T) {
	ctl := newFakeCtl(t)
	b := newBlocker()
	b.ignore = true
	startAgent(t, ctl, []pluginsdk.Plugin{plug(b.run)}, func(c *Config) { c.Slots = 1 })
	ctl.mustExecute(stepReq("a1"))
	b.waitStarted(t)
	ctl.cancel("a1", 30)
	c := completeOf(t, ctl, "a1")
	if c.Status != protocol.StatusCanceled {
		t.Errorf("status = %q, want canceled even though the plugin ignored ctx", c.Status)
	}
	// The stuck plugin still holds its slot.
	res, _ := ctl.execute(stepReq("a2"))
	if res.Accepted {
		t.Error("slot reused while the abandoned plugin still runs")
	}
	close(b.release)
	eventually(t, "slot released", func() bool {
		hb, ok := ctl.lastBeat()
		return ok && hb.FreeSlots == 1
	})
}

func TestTimeout(t *testing.T) {
	ctl := newFakeCtl(t)
	b := newBlocker()
	startAgent(t, ctl, []pluginsdk.Plugin{plug(b.run)})
	req := stepReq("a1")
	req.TimeoutMS = 40
	ctl.mustExecute(req)
	c := completeOf(t, ctl, "a1")
	if c.Status != protocol.StatusTimedOut || c.Error == nil || c.Error.Code != "timeout" {
		t.Errorf("complete = %+v", c)
	}
}

func TestStopCancelsRunningSteps(t *testing.T) {
	ctl := newFakeCtl(t)
	b := newBlocker()
	a, _ := startAgent(t, ctl, []pluginsdk.Plugin{plug(b.run)})
	ctl.mustExecute(stepReq("a1"))
	b.waitStarted(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPluginReceivesRequestFields(t *testing.T) {
	ctl := newFakeCtl(t)
	ctl.autoAck.Store(true)
	got := make(chan pluginsdk.ExecRequest, 1)
	p := plug(func(_ context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
		emit.Progress(150, "working")
		got <- req
		return pluginsdk.Result{}, nil
	})
	startAgent(t, ctl, []pluginsdk.Plugin{p}, func(c *Config) {
		c.PluginConfig = map[string]map[string]string{"demo": {"allow_insecure_host_keys": "true"}}
	})
	req := stepReq("a1")
	req.Input = map[string]any{"k": "v", "n": int64(7)}
	req.Nodes = map[string]pluginsdk.NodeInfo{"web1": {Name: "web1", Address: "10.0.0.1", Port: 22}}
	ctl.mustExecute(req)
	r := <-got
	if string(r.Input) != `{"k":"v","n":7}` {
		t.Errorf("input = %s", r.Input)
	}
	if r.Nodes["web1"].Address != "10.0.0.1" || r.Config["allow_insecure_host_keys"] != "true" || r.WorkDir == "" || r.Logger == nil || r.HostKeys == nil {
		t.Errorf("request = %+v", r)
	}
	completeOf(t, ctl, "a1")
	eventually(t, "progress forwarded clamped", func() bool {
		ps := ctl.progressList()
		return len(ps) == 1 && ps[0].Percent == 100 && ps[0].Message == "working"
	})
}
