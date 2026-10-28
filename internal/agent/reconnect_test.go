package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// results is the controller's idea of applied results: step.complete is
// applied at most once per attempt, later deliveries are answered stale.
type results struct {
	mu      sync.Mutex
	applied map[string]int
}

func (s *results) apply(attempt string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applied == nil {
		s.applied = map[string]int{}
	}
	s.applied[attempt]++
	return s.applied[attempt]
}

func (s *results) count(attempt string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applied[attempt]
}

func succeed() testPlugin {
	return plug(func(_ context.Context, _ pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
		emit.Log(pluginsdk.Stdout, "done")
		return pluginsdk.Result{}, nil
	})
}

func (c *fakeCtl) waitHellos(n int) []protocol.HelloRequest {
	c.t.Helper()
	eventually(c.t, fmt.Sprintf("%d hellos", n), func() bool { return len(c.helloList()) >= n })
	return c.helloList()
}

// TestCompleteRetriedAcrossReconnect: the controller applies the result but
// the connection dies before the acknowledgement. The agent keeps the result,
// lists it as finished in the next hello and resends; the store sees it as
// stale and applies it once. The agent then stops retrying.
func TestCompleteRetriedAcrossReconnect(t *testing.T) {
	ctl := newFakeCtl(t)
	st := &results{}
	ctl.onComplete = func(n int, fc *fakeConn, sc protocol.StepComplete) (protocol.StepCompleteAck, error) {
		first := st.apply(sc.AttemptID) == 1
		if n == 1 {
			_ = fc.peer.Close() // the ack is lost
			return protocol.StepCompleteAck{}, context.Canceled
		}
		return protocol.StepCompleteAck{Acknowledged: true, Stale: !first}, nil
	}
	startAgent(t, ctl, []pluginsdk.Plugin{succeed()})
	ctl.mustExecute(stepReq("a1"))

	hellos := ctl.waitHellos(2)
	if got := hellos[1].Executions; len(got) != 1 || got[0].AttemptID != "a1" || got[0].State != protocol.ExecFinished {
		t.Errorf("second hello executions = %+v, want a1 finished", got)
	}
	if len(hellos[0].Executions) != 0 {
		t.Errorf("first hello executions = %+v", hellos[0].Executions)
	}
	eventually(t, "second delivery", func() bool { return len(ctl.completeList()) == 2 })
	time.Sleep(150 * time.Millisecond) // an acknowledged result is not sent again
	if n := len(ctl.completeList()); n != 2 {
		t.Errorf("step.complete delivered %d times, want 2", n)
	}
	if st.count("a1") != 2 {
		t.Errorf("store saw %d deliveries", st.count("a1"))
	}
	if ctl.lines("a1")[1].Line != "done" {
		t.Errorf("logs = %v", ctl.lines("a1"))
	}
	eventually(t, "no more finished attempts after the ack", func() bool {
		return len(ctl.latestHelloExecutions(t)) == 0
	})
}

// latestHelloExecutions forces a reconnect and returns what the agent lists.
func (c *fakeCtl) latestHelloExecutions(t *testing.T) []protocol.ExecutionState {
	t.Helper()
	before := len(c.helloList())
	_ = c.latest().peer.Close()
	hs := c.waitHellos(before + 1)
	return hs[len(hs)-1].Executions
}

// A result stays pending while the controller keeps failing it, and a
// reconciliation "cancel" for a finished attempt discards it.
func TestReconciliationDiscardsFinishedResult(t *testing.T) {
	ctl := newFakeCtl(t)
	ctl.onComplete = func(n int, fc *fakeConn, _ protocol.StepComplete) (protocol.StepCompleteAck, error) {
		if n == 1 {
			_ = fc.peer.Close()
		}
		return protocol.StepCompleteAck{}, context.Canceled
	}
	ctl.helloResp = func(n int, _ protocol.HelloRequest) protocol.HelloResponse {
		resp := protocol.HelloResponse{Protocol: protocol.ProtocolVersion, HeartbeatIntervalMS: 20}
		if n >= 2 {
			resp.Reconciliation = []protocol.Reconciliation{{AttemptID: "a1", Action: protocol.ActionCancel}}
		}
		return resp
	}
	startAgent(t, ctl, []pluginsdk.Plugin{succeed()})
	ctl.mustExecute(stepReq("a1"))
	ctl.waitHellos(2)
	time.Sleep(150 * time.Millisecond)
	if n := len(ctl.completeList()); n != 1 {
		t.Errorf("step.complete delivered %d times; the discarded result was resent", n)
	}
}

func TestStaleAckStopsRetries(t *testing.T) {
	ctl := newFakeCtl(t)
	ctl.onComplete = func(int, *fakeConn, protocol.StepComplete) (protocol.StepCompleteAck, error) {
		return protocol.StepCompleteAck{Stale: true}, nil
	}
	startAgent(t, ctl, []pluginsdk.Plugin{succeed()})
	ctl.mustExecute(stepReq("a1"))
	completeOf(t, ctl, "a1")
	time.Sleep(150 * time.Millisecond)
	if n := len(ctl.completeList()); n != 1 {
		t.Errorf("delivered %d times after a stale ack", n)
	}
}

func TestUnacknowledgedResultIsRetried(t *testing.T) {
	ctl := newFakeCtl(t)
	st := &results{}
	ctl.onComplete = func(_ int, _ *fakeConn, sc protocol.StepComplete) (protocol.StepCompleteAck, error) {
		if st.apply(sc.AttemptID) < 3 {
			return protocol.StepCompleteAck{}, context.Canceled // an error response
		}
		return protocol.StepCompleteAck{Acknowledged: true}, nil
	}
	startAgent(t, ctl, []pluginsdk.Plugin{succeed()})
	ctl.mustExecute(stepReq("a1"))
	eventually(t, "third delivery acknowledged", func() bool { return st.count("a1") == 3 })
	time.Sleep(100 * time.Millisecond)
	if st.count("a1") != 3 {
		t.Errorf("deliveries = %d, want 3", st.count("a1"))
	}
}

// TestRunningAttemptSurvivesReconnect covers the controller's two decisions
// for a running attempt after a reconnect.
func TestRunningAttemptSurvivesReconnect(t *testing.T) {
	tests := []struct {
		action string
		want   string
	}{
		{protocol.ActionContinue, protocol.StatusSucceeded},
		{protocol.ActionCancel, protocol.StatusCanceled},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			ctl := newFakeCtl(t)
			ctl.helloResp = func(n int, req protocol.HelloRequest) protocol.HelloResponse {
				resp := protocol.HelloResponse{Protocol: protocol.ProtocolVersion, HeartbeatIntervalMS: 20}
				if n >= 2 {
					resp.Reconciliation = []protocol.Reconciliation{{AttemptID: "a1", Action: tt.action}}
				}
				return resp
			}
			b := newBlocker()
			startAgent(t, ctl, []pluginsdk.Plugin{plug(b.run)})
			ctl.mustExecute(stepReq("a1"))
			b.waitStarted(t)
			_ = ctl.latest().peer.Close()
			hs := ctl.waitHellos(2)
			if got := hs[1].Executions; len(got) != 1 || got[0].AttemptID != "a1" || got[0].State != protocol.ExecRunning {
				t.Errorf("hello executions = %+v, want a1 running", got)
			}
			if tt.action == protocol.ActionContinue {
				time.Sleep(50 * time.Millisecond)
				if len(ctl.completeList()) != 0 {
					t.Fatal("a continued attempt completed early")
				}
				close(b.release)
			}
			if c := completeOf(t, ctl, "a1"); c.Status != tt.want {
				t.Errorf("status = %q, want %q", c.Status, tt.want)
			}
		})
	}
}

// Unacknowledged logs are resent after a reconnect and freed once acknowledged.
func TestLogsResentAfterReconnectAndFreedByAck(t *testing.T) {
	ctl := newFakeCtl(t)
	release := make(chan struct{})
	started := make(chan struct{})
	p := plug(func(_ context.Context, _ pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
		for i := range 5 {
			emit.Log(pluginsdk.Stdout, fmt.Sprintf("early %d", i))
		}
		close(started)
		<-release
		emit.Log(pluginsdk.Stdout, "late")
		return pluginsdk.Result{}, nil
	})
	a, _ := startAgent(t, ctl, []pluginsdk.Plugin{p})
	ctl.mustExecute(stepReq("a1"))
	<-started
	eventually(t, "first batch", func() bool { return len(ctl.lines("a1")) == 5 })

	before := len(ctl.logsOf("a1"))
	_ = ctl.latest().peer.Close()
	ctl.waitHellos(2)
	eventually(t, "unacked batch resent", func() bool {
		ls := ctl.logsOf("a1")
		return len(ls) > before && ls[before].SeqFrom == 1 && len(ls[before].Entries) == 5
	})
	if a.exec.hub.batchCount("a1") == 0 {
		t.Fatal("batches freed without an ack")
	}
	ctl.notify(protocol.MethodLogAck, protocol.LogAck{AttemptID: "a1", Seq: 5})
	eventually(t, "buffer freed by the ack", func() bool { return a.exec.hub.batchCount("a1") == 0 && a.exec.hub.memUsed() == 0 })

	close(release)
	completeOf(t, ctl, "a1")
	if got := ctl.lines("a1")[6].Line; got != "late" {
		t.Errorf("entry 6 = %q, want the line logged after the reconnect (seq continues)", got)
	}
}

// TestSpoolOverflowEndToEnd: with a tiny buffer and no acks, the oldest
// batches are dropped, a System line records the gap, and the heartbeat
// reports the spool size within its bound.
func TestSpoolOverflowEndToEnd(t *testing.T) {
	const spoolCap = 2500
	ctl := newFakeCtl(t)
	release := make(chan struct{})
	logged := make(chan struct{})
	p := plug(func(_ context.Context, _ pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
		for i := range 40 {
			emit.Log(pluginsdk.Stdout, fmt.Sprintf("n%02d %s", i, strings.Repeat("z", 150)))
			time.Sleep(15 * time.Millisecond) // one batch per line
		}
		close(logged)
		<-release
		return pluginsdk.Result{}, nil
	})
	startAgent(t, ctl, []pluginsdk.Plugin{p}, func(c *Config) {
		c.MemBufferBytes, c.SpoolBytes = 1, spoolCap
	})
	ctl.mustExecute(stepReq("a1"))
	<-logged
	eventually(t, "gap line delivered", func() bool {
		for _, e := range ctl.lines("a1") {
			if e.Stream == pluginsdk.System {
				return true
			}
		}
		return false
	})

	var gap string
	for _, e := range ctl.lines("a1") {
		if e.Stream == pluginsdk.System && strings.HasPrefix(e.Line, "log gap:") {
			gap = e.Line
		}
	}
	if gap == "" {
		t.Fatal("no System line records the dropped logs")
	}
	eventually(t, "heartbeat reports a bounded spool", func() bool {
		hb, ok := ctl.lastBeat()
		return ok && hb.SpoolBytes > 0 && hb.SpoolBytes <= spoolCap
	})

	// After a reconnect only the surviving batches are resent; the oldest
	// entries are gone for good.
	before := len(ctl.logsOf("a1"))
	_ = ctl.latest().peer.Close()
	ctl.waitHellos(2)
	eventually(t, "resend", func() bool { return len(ctl.logsOf("a1")) > before })
	for _, la := range ctl.logsOf("a1")[before:] {
		if la.SeqFrom == 1 {
			t.Errorf("batch from seq 1 was resent although it should have been dropped")
		}
	}
	close(release)
	completeOf(t, ctl, "a1")
}

func TestHostKeysAreForwarded(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	ctl := newFakeCtl(t)
	p := plug(func(_ context.Context, req pluginsdk.ExecRequest, _ pluginsdk.Emitter) (pluginsdk.Result, error) {
		req.HostKeys.ReportHostKey("web1", key+" comment")
		req.HostKeys.ReportHostKey("web2", "not a key")
		return pluginsdk.Result{}, nil
	})
	startAgent(t, ctl, []pluginsdk.Plugin{p})
	ctl.mustExecute(stepReq("a1"))
	completeOf(t, ctl, "a1")
	hks := ctl.hostKeyList()
	if len(hks) != 1 {
		t.Fatalf("host keys = %+v, want only the valid one", hks)
	}
	hk := hks[0]
	if hk.AttemptID != "a1" || hk.Node != "web1" || hk.KeyType != "ssh-ed25519" || !strings.HasPrefix(hk.Fingerprint, "SHA256:") || !strings.HasPrefix(hk.HostKey, key) {
		t.Errorf("host key report = %+v", hk)
	}
}
