package proc_test

import (
	"context"
	"os"
	osexec "os/exec"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
	"github.com/denyszorinets/damson/plugins/internal/proc"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 {
		if err := goleak.Find(); err != nil {
			_, _ = os.Stderr.WriteString("goleak: " + err.Error() + "\n")
			code = 1
		}
	}
	os.Exit(code)
}

func spec(script string) proc.Spec {
	return proc.Spec{Env: []string{"PATH=" + os.Getenv("PATH")}, Name: "sh", Args: []string{"-c", script}}
}

func TestRunStreamsOutputAndKeepsTheStderrTail(t *testing.T) {
	em := &plugintest.FakeEmitter{}
	res, err := proc.Run(context.Background(), spec("echo out1; echo err1 >&2; echo err2 >&2; exit 3"), em)
	if err != nil || res.ExitCode != 3 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if !strings.Contains(res.Stderr, "err1") || !strings.Contains(res.Stderr, "err2") || strings.Contains(res.Stderr, "out1") {
		t.Errorf("stderr tail = %q", res.Stderr)
	}
	if text := em.Text(); !strings.Contains(text, "out1") || !strings.Contains(text, "err2") {
		t.Errorf("log = %q", text)
	}
}

func TestLongLinesAreSplit(t *testing.T) {
	em := &plugintest.FakeEmitter{}
	res, err := proc.Run(context.Background(), spec("head -c 600000 /dev/zero | tr '\\0' x; echo"), em)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	total := 0
	for _, l := range em.Lines() {
		if len(l.Line) > 300<<10 {
			t.Fatalf("a log line has %d bytes", len(l.Line))
		}
		total += len(l.Line)
	}
	if total != 600000 {
		t.Errorf("logged %d bytes, want 600000", total)
	}
}

// A child that leaves the process group and keeps the output open must not make
// Run hang after a cancel (the hard rule on honoring ctx).
func TestCancelDoesNotHangOnAnEscapedChild(t *testing.T) {
	if _, err := osexec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not installed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	_, err := proc.Run(ctx, spec("setsid sleep 20 & sleep 20"), &plugintest.FakeEmitter{})
	if se := pluginsdk.AsStepError(err); se == nil || se.Kind != pluginsdk.KindCancelled {
		t.Fatalf("cancel: %v", err)
	}
	if d := time.Since(start); d > 12*time.Second {
		t.Fatalf("Run waited %s for a child that escaped the group", d)
	}
}

// With a grace period a cancel sends SIGTERM first, so the program can clean up.
func TestGracefulCancelLetsTheProgramFinish(t *testing.T) {
	marker := t.TempDir() + "/terminated"
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(500*time.Millisecond, cancel)
	s := spec("trap 'echo done > " + marker + "; exit 0' TERM; sleep 30 & wait")
	s.Grace = 10 * time.Second
	_, err := proc.Run(ctx, s, &plugintest.FakeEmitter{})
	if se := pluginsdk.AsStepError(err); se == nil || se.Kind != pluginsdk.KindCancelled {
		t.Fatalf("cancel: %v", err)
	}
	if _, serr := os.Stat(marker); serr != nil {
		t.Fatalf("the program did not get SIGTERM and time to clean up: %v", serr)
	}
}

func TestMissingProgramAndCancellation(t *testing.T) {
	_, err := proc.Run(context.Background(), proc.Spec{Env: []string{"PATH=/nonexistent"}, Name: "no-such-tool"}, &plugintest.FakeEmitter{})
	if se := pluginsdk.AsStepError(err); se == nil || se.Kind != pluginsdk.KindInternal || !strings.Contains(se.Message, "not installed") {
		t.Fatalf("missing program: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	// The child starts a grandchild; both must die with the group.
	_, err = proc.Run(ctx, spec("sleep 30 & sleep 30; wait"), &plugintest.FakeEmitter{})
	if se := pluginsdk.AsStepError(err); se == nil || se.Kind != pluginsdk.KindCancelled {
		t.Fatalf("cancel: %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("cancel took %s", d)
	}

	tctx, tcancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer tcancel()
	_, err = proc.Run(tctx, spec("sleep 30"), &plugintest.FakeEmitter{})
	if se := pluginsdk.AsStepError(err); se == nil || se.Kind != pluginsdk.KindTimeout {
		t.Fatalf("timeout: %v", err)
	}
}

// A context that ended before the command could start is a cancellation (or a
// timeout), never a failure to start. Canceling in the gap between two steps
// of a plugin hit this and made cancel_blocking of the contract flaky.
func TestContextEndedBeforeStartIsNotAStartFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := proc.Run(ctx, spec("true"), &plugintest.FakeEmitter{})
	if se := pluginsdk.AsStepError(err); se == nil || se.Kind != pluginsdk.KindCancelled {
		t.Fatalf("cancelled before start: %v", err)
	}
	tctx, tcancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer tcancel()
	_, err = proc.Run(tctx, spec("true"), &plugintest.FakeEmitter{})
	if se := pluginsdk.AsStepError(err); se == nil || se.Kind != pluginsdk.KindTimeout {
		t.Fatalf("deadline passed before start: %v", err)
	}
}
