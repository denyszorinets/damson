package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// newBareExecutor builds an executor that is not started and has no link.
func newBareExecutor(t *testing.T) *Executor {
	t.Helper()
	a, err := New(Config{
		ControllerURL: "ws://127.0.0.1:1", Token: "t", AgentID: "x", Pool: "p",
		Logger: slog.New(slog.DiscardHandler), Sampler: &fakeSampler{}, SpoolDir: t.TempDir(), WorkRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	e := a.exec
	e.base = t.Context()
	t.Cleanup(e.hub.close)
	return e
}

func TestAckedStepKeepsUnsentLogs(t *testing.T) {
	e := newBareExecutor(t)
	e.hub.register("a1")
	e.hub.add("a1", 1, entries(2, 10)) // never sent: there is no link
	e.done["a1"] = &finishedStep{msg: protocol.StepComplete{AttemptID: "a1"}, at: time.Now()}

	e.markAcked("a1")
	e.settle("a1")
	if _, ok := e.done["a1"]; !ok || e.hub.batchCount("a1") != 1 {
		t.Fatalf("acked step with unsent logs was dropped: done=%v batches=%d", ok, e.hub.batchCount("a1"))
	}
	if st := e.States(); len(st) != 0 {
		t.Errorf("an acknowledged step is still listed in hello: %+v", st)
	}
	if b, _ := e.hub.nextUnsent("a1"); b == nil { // the batch goes out
		t.Fatal("expected an unsent batch")
	}
	e.settle("a1")
	if _, ok := e.done["a1"]; ok || e.hub.batchCount("a1") != 0 {
		t.Errorf("step not dropped once its logs were sent: done=%v batches=%d", ok, e.hub.batchCount("a1"))
	}
	if !e.completed.has("a1", e.cfg.Now()) {
		t.Error("dropped attempt is not remembered as completed")
	}
}

func TestBadAckForOneResultDoesNotBlockTheNext(t *testing.T) {
	cases := map[string]func(protocol.StepComplete) (protocol.StepCompleteAck, error){
		"rpc error": func(protocol.StepComplete) (protocol.StepCompleteAck, error) {
			return protocol.StepCompleteAck{}, &protocol.RPCError{Code: protocol.CodeInternalError, Message: "boom"}
		},
		"malformed ack": func(protocol.StepComplete) (protocol.StepCompleteAck, error) {
			return protocol.StepCompleteAck{}, nil
		},
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			ctl := newFakeCtl(t)
			ctl.onComplete = func(_ int, _ *fakeConn, sc protocol.StepComplete) (protocol.StepCompleteAck, error) {
				if sc.AttemptID == "bad" {
					return bad(sc)
				}
				return protocol.StepCompleteAck{Acknowledged: true}, nil
			}
			a, _ := startAgent(t, ctl, []pluginsdk.Plugin{plug(func(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error) {
				return pluginsdk.Result{}, nil
			})})
			ctl.mustExecute(stepReq("bad"))
			completeOf(t, ctl, "bad")
			ctl.mustExecute(stepReq("good"))
			eventually(t, "good result acknowledged while bad keeps failing", func() bool {
				st := a.exec.States()
				return len(st) == 1 && st[0].AttemptID == "bad"
			})
			eventually(t, "bad result backs off", func() bool {
				a.exec.mu.Lock()
				defer a.exec.mu.Unlock()
				f := a.exec.done["bad"]
				return f != nil && f.failures >= 1 && !f.retryAt.IsZero()
			})
		})
	}
}

func TestCompletedAttemptIsNotRunAgain(t *testing.T) {
	ctl := newFakeCtl(t)
	var runs atomic.Int32
	a, _ := startAgent(t, ctl, []pluginsdk.Plugin{plug(func(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error) {
		runs.Add(1)
		return pluginsdk.Result{}, nil
	})})
	ctl.mustExecute(stepReq("a1"))
	completeOf(t, ctl, "a1")
	eventually(t, "result acknowledged and dropped", func() bool { return len(a.exec.States()) == 0 })
	res, err := ctl.execute(stepReq("a1"))
	if err != nil || res.Accepted || res.Reason != "already completed" {
		t.Errorf("replayed execute = %+v, %v; want rejected as already completed", res, err)
	}
	if runs.Load() != 1 {
		t.Errorf("plugin ran %d times, want 1", runs.Load())
	}
}

func TestReconcileDiscardRemembersAttempt(t *testing.T) {
	e := newBareExecutor(t)
	e.done["a1"] = &finishedStep{msg: protocol.StepComplete{AttemptID: "a1"}, at: time.Now()}
	e.Reconcile([]protocol.Reconciliation{{AttemptID: "a1", Action: protocol.ActionCancel}})
	if !e.completed.has("a1", e.cfg.Now()) {
		t.Error("discarded attempt is not remembered")
	}
}

func TestFinishedSetIsBounded(t *testing.T) {
	e := newBareExecutor(t)
	now := time.Now()
	for i := range maxFinished + 6 {
		id := fmt.Sprintf("a%d", i)
		e.done[id] = &finishedStep{at: now, seq: uint64(i)}
		e.hub.register(id)
	}
	e.done["old"] = &finishedStep{at: now.Add(-maxFinishedAge - time.Minute), seq: 1 << 40}
	dropped := e.pruneDoneLocked(now)
	if len(e.done) != maxFinished {
		t.Errorf("done has %d entries, want %d", len(e.done), maxFinished)
	}
	if len(dropped) != 7 || !e.completed.has("a0", now) || !e.completed.has("old", now) || e.completed.has("a6", now) {
		t.Errorf("wrong victims: %v", dropped)
	}
}

func TestSanitizeOutputsLimit(t *testing.T) {
	big, _ := json.Marshal(map[string]string{"v": strings.Repeat("a", maxOutputBytes+1)})
	if _, _, err := sanitizeOutputs(big, newMasker(nil)); err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Errorf("err = %v, want a limit error", err)
	}
	ok, _ := json.Marshal(map[string]string{"v": strings.Repeat("a", maxOutputBytes/2)})
	out, _, err := sanitizeOutputs(ok, newMasker(nil))
	if err != nil {
		t.Fatal(err)
	}
	// The largest accepted result, with the longest error message, fits a frame.
	msg := protocol.StepComplete{AttemptID: "a", Status: protocol.StatusFailed, Outputs: out,
		Error: &protocol.StepError{Message: truncate(strings.Repeat("e", 1<<20), maxErrorBytes)}}
	if len(msg.Error.Message) > maxErrorBytes+32 {
		t.Errorf("error message not truncated: %d", len(msg.Error.Message))
	}
	raw, err := protocol.Encode(msg)
	if err != nil || len(raw) > protocol.MaxFrameSize/2 {
		t.Errorf("complete is %d bytes (%v); want far below the frame limit", len(raw), err)
	}
}

func TestOversizedOutputsFailTheStep(t *testing.T) {
	ctl := newFakeCtl(t)
	p := plug(func(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error) {
		out, _ := json.Marshal(map[string]string{"v": strings.Repeat("a", 2*maxOutputBytes)})
		return pluginsdk.Result{Outputs: out}, nil
	})
	startAgent(t, ctl, []pluginsdk.Plugin{p})
	ctl.mustExecute(stepReq("a1"))
	c := completeOf(t, ctl, "a1")
	if c.Status != protocol.StatusFailed || c.Error == nil || !strings.Contains(c.Error.Message, "exceed") || c.Outputs != nil {
		t.Errorf("complete = %+v", c)
	}
}

func TestBuildCompleteRecoversFromPanic(t *testing.T) {
	e := newBareExecutor(t)
	// A run without a context makes buildComplete panic.
	msg := e.safeComplete(&run{attempt: "a1"}, pluginOutcome{}, newMasker(nil), nil, time.Now())
	if msg.Status != protocol.StatusFailed || msg.Error == nil || msg.Error.Code != "internal" || msg.AttemptID != "a1" {
		t.Errorf("msg = %+v", msg)
	}
}

func TestLogPipeSplitMakesProgressOnBinaryInput(t *testing.T) {
	h := newTestHub(t, 1<<30, 1<<20)
	p := newLogPipe(t.Context(), h, "a", newMasker(nil), time.Now, time.Hour, Clock{})
	p.Log(pluginsdk.Stdout, strings.Repeat("\x80", 3*maxLineBytes+5)) // continuation bytes only
	p.flush()
	h.mu.Lock()
	defer h.mu.Unlock()
	var n int
	for _, b := range h.attempts["a"].batches {
		for _, e := range b.entries {
			if len(e.Line) > maxLineBytes || len(e.Line) == 0 {
				t.Errorf("entry of %d bytes", len(e.Line))
			}
			n += len(e.Line)
		}
	}
	if n != 3*maxLineBytes+5 {
		t.Errorf("kept %d bytes", n)
	}
}

func pipeText(t *testing.T, h *logHub, attempt string) []string {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, b := range h.attempts[attempt].batches {
		es, err := h.loadLocked(b)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			out = append(out, e.Line)
		}
	}
	return out
}

func TestSecretStraddlingTheSplitPointIsMasked(t *testing.T) {
	const secret = "STRADDLE-SECRET-VALUE"
	m := newMasker(protocol.SecretBundle{"r": {"v": secret}})
	h := newTestHub(t, 1<<30, 1<<20)
	p := newLogPipe(t.Context(), h, "a", m, time.Now, time.Hour, Clock{})
	for _, pad := range []int{8, 11, 13, 20} { // the secret crosses the 32 KiB cut at different offsets
		p.Log(pluginsdk.Stdout, strings.Repeat("x", maxLineBytes-pad)+secret+strings.Repeat("y", 100))
	}
	p.flush()
	for _, l := range pipeText(t, h, "a") {
		if strings.Contains(l, "STRADDLE") || strings.Contains(l, "VALUE") || strings.Contains(l, "SECRET") {
			t.Fatalf("a fragment of the secret survived the split: %.40q", l)
		}
	}
}

func TestMaskerOverlappingSecrets(t *testing.T) {
	m := newMasker(protocol.SecretBundle{"r": {"a": "abcd", "b": "cdef"}})
	if got := m.Mask("abcdef"); got != "***" {
		t.Errorf("Mask(abcdef) = %q, want ***", got)
	}
	if got := m.Mask("zabcdefz xcdefx yabcdy"); got != "z***z x***x y***y" {
		t.Errorf("Mask = %q", got)
	}
}

func TestMaskerShortSecretsAreNotMaskedButNoted(t *testing.T) {
	m := newMasker(protocol.SecretBundle{"r": {"a": "ab", "b": "xyz", "c": ""}})
	if !m.short {
		t.Error("short value not noted")
	}
	if got := m.Mask("ab xyz abxyz"); got != "ab xyz abxyz" {
		t.Errorf("short secrets must not be masked in text, got %q", got)
	}
	if newMasker(protocol.SecretBundle{"r": {"a": ""}}).short {
		t.Error("an empty value counts as short")
	}
	if !newMasker(protocol.SecretBundle{"r": {"a": "abcd"}}).Contains("..abcd..") {
		t.Error("a 4 byte secret must be masked")
	}
}

func TestMaskerWipeFailsClosed(t *testing.T) {
	m := newMasker(protocol.SecretBundle{"r": {"a": "abcdefgh"}})
	m.wipe()
	if got := m.Mask("hello"); got != maskText {
		t.Errorf("wiped masker returned %q; text could leak through a late plugin goroutine", got)
	}
	if m.Mask("") != "" {
		t.Error("empty text changed")
	}
}

func TestShortSecretGetsOneSystemLinePerStep(t *testing.T) {
	ctl := newFakeCtl(t)
	ctl.autoAck.Store(true)
	p := plug(func(_ context.Context, _ pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
		emit.Log(pluginsdk.Stdout, "ab c ab")
		return pluginsdk.Result{}, nil
	})
	_, agentLog := startAgent(t, ctl, []pluginsdk.Plugin{p})
	req := stepReq("a1")
	req.Secrets = protocol.SecretBundle{"r": {"user": "ab", "x": "c", "y": "zz"}}
	ctl.mustExecute(req)
	completeOf(t, ctl, "a1")
	var sys int
	for _, e := range ctl.lines("a1") {
		if e.Stream == pluginsdk.System {
			sys++
			if e.Line != shortSecretLine {
				t.Errorf("system line = %q", e.Line)
			}
		} else if e.Line != "ab c ab" {
			t.Errorf("line = %q", e.Line)
		}
	}
	if sys != 1 {
		t.Errorf("%d short-secret lines, want 1", sys)
	}
	if strings.Contains(agentLog.String(), "zz") {
		t.Error("agent log names a short value")
	}
}

func TestSecretSplitAcrossLogCallsIsAKnownLimit(t *testing.T) {
	m := newMasker(protocol.SecretBundle{"r": {"v": "password1234"}})
	h := newTestHub(t, 1<<30, 1<<20)
	p := newLogPipe(t.Context(), h, "a", m, time.Now, time.Hour, Clock{})
	p.Log(pluginsdk.Stdout, "pass")
	p.Log(pluginsdk.Stdout, "word1234")
	p.Log(pluginsdk.Stdout, "password1234")
	p.flush()
	got := pipeText(t, h, "a")
	if len(got) != 3 || got[0] != "pass" || got[1] != "word1234" || got[2] != "***" {
		t.Errorf("lines = %q; plugins must log whole lines (documented limitation)", got)
	}
}

func TestLogPipeRejectsLinesAfterClose(t *testing.T) {
	h := newTestHub(t, 1<<30, 1<<20)
	p := newLogPipe(t.Context(), h, "a", newMasker(nil), time.Now, time.Hour, Clock{})
	p.start()
	p.Log(pluginsdk.Stdout, "kept")
	p.close()
	p.Log(pluginsdk.Stdout, "late")
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pending) != 0 {
		t.Errorf("late line queued: %v", p.pending)
	}
	if got := pipeTextLocked(h, "a"); len(got) != 1 || got[0] != "kept" {
		t.Errorf("lines = %v", got)
	}
}

func pipeTextLocked(h *logHub, attempt string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, b := range h.attempts[attempt].batches {
		for _, e := range b.entries {
			out = append(out, e.Line)
		}
	}
	return out
}

func TestSpoolFileNeverHoldsASecret(t *testing.T) {
	ctl := newFakeCtl(t)
	release := make(chan struct{})
	p := plug(func(_ context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
		pw, _ := req.Secrets.Resolve("secret://t/pw")
		emit.Log(pluginsdk.Stdout, "password is "+pw)
		emit.Log(pluginsdk.Stdout, "key "+testKeyLine1)
		<-release
		return pluginsdk.Result{}, nil
	})
	a, _ := startAgent(t, ctl, []pluginsdk.Plugin{p}, func(c *Config) { c.MemBufferBytes = 1 }) // force the spill
	req := stepReq("a1")
	req.Secrets = testBundle()
	ctl.mustExecute(req)
	eventually(t, "logs spilled", func() bool { return a.exec.SpoolBytes() > 0 })

	h := a.exec.hub
	h.mu.Lock()
	fi, err := h.sp.f.Stat()
	if err != nil {
		h.mu.Unlock()
		t.Fatal(err)
	}
	raw, err := io.ReadAll(io.NewSectionReader(h.sp.f, 0, fi.Size()))
	h.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "password is ***") {
		t.Errorf("spool does not hold the masked line: %q", raw)
	}
	for _, needle := range leaks() {
		if needle != "" && strings.Contains(string(raw), needle) {
			t.Fatalf("the spool file contains a secret form (%d bytes)", len(needle))
		}
	}
	close(release)
	completeOf(t, ctl, "a1")
}

func TestHostKeyReportsAreMasked(t *testing.T) {
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
		req.HostKeys.ReportHostKey("node-"+testPassword, key+" comment "+testPassword)
		return pluginsdk.Result{}, nil
	})
	_, agentLog := startAgent(t, ctl, []pluginsdk.Plugin{p})
	r := stepReq("a1")
	r.Secrets = testBundle()
	ctl.mustExecute(r)
	completeOf(t, ctl, "a1")
	eventually(t, "host key", func() bool { return len(ctl.hostKeyList()) == 1 })
	hk := ctl.hostKeyList()[0]
	if hk.Node != "node-***" || hk.HostKey != key {
		t.Errorf("host key report = %+v", hk)
	}
	assertNoLeak(t, ctl, agentLog)
}

func TestStopClosesSpoolEvenWhenPluginIgnoresCancel(t *testing.T) {
	ctl := newFakeCtl(t)
	release := make(chan struct{})
	started := make(chan struct{})
	p := plug(func(_ context.Context, _ pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
		emit.Log(pluginsdk.Stdout, "hello")
		close(started)
		<-release // ignores ctx
		return pluginsdk.Result{}, nil
	})
	a, _ := startAgent(t, ctl, []pluginsdk.Plugin{p}, func(c *Config) { c.MemBufferBytes = 1 })
	ctl.mustExecute(stepReq("a1"))
	<-started
	eventually(t, "logs spilled", func() bool { return a.exec.SpoolBytes() > 0 })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := a.Stop(ctx); err == nil {
		t.Error("Stop reported success while a plugin still runs")
	}
	a.exec.hub.mu.Lock()
	closed := a.exec.hub.sp == nil
	a.exec.hub.mu.Unlock()
	if !closed {
		t.Error("spool left open after a timed-out Stop")
	}
	close(release)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := a.Stop(ctx2); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestStaleSpoolFilesAreSweptAtStart(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "damson-spool-crashed.bin")
	if err := os.WriteFile(stale, []byte("old masked output"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctl := newFakeCtl(t)
	startAgent(t, ctl, nil, func(c *Config) { c.SpoolDir = dir })
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale spool file still there: %v", err)
	}
}

func TestShorterLaterCancelGraceIsHonoured(t *testing.T) {
	ctl := newFakeCtl(t)
	b := newBlocker()
	b.ignore = true
	startAgent(t, ctl, []pluginsdk.Plugin{plug(b.run)}, func(c *Config) { c.CancelGrace = time.Minute })
	t.Cleanup(func() { close(b.release) }) // runs before the agent's Stop
	req := stepReq("a1")
	req.TimeoutMS = 30
	ctl.mustExecute(req)
	b.waitStarted(t)
	time.Sleep(150 * time.Millisecond) // the timeout fired; the minute-long grace is running
	ctl.cancel("a1", 50)
	c := completeOf(t, ctl, "a1")
	if c.Status != protocol.StatusTimedOut {
		t.Errorf("status = %q", c.Status)
	}
}
