package proc_test

import (
	"context"
	"os"
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
