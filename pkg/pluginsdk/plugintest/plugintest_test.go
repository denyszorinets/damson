package plugintest_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
)

func TestSourceProblems(t *testing.T) {
	tests := []struct {
		dir  string
		want []string // substrings, one per expected problem
	}{
		{"testdata/good", nil},
		{"testdata/bad", []string{"forbidden import", "init() is forbidden", `var "counter"`, "missing func Plugins"}},
	}
	for _, tc := range tests {
		t.Run(tc.dir, func(t *testing.T) {
			got := plugintest.SourceProblems(t, tc.dir)
			if len(got) != len(tc.want) {
				t.Fatalf("problems = %q, want %d", got, len(tc.want))
			}
			for _, w := range tc.want {
				found := false
				for _, g := range got {
					found = found || strings.Contains(g, w)
				}
				if !found {
					t.Errorf("no problem containing %q in %q", w, got)
				}
			}
		})
	}
}

func TestFakes(t *testing.T) {
	e := &plugintest.FakeEmitter{}
	e.Log(pluginsdk.Stderr, "a")
	e.Progress(0.5, "half")
	if l := e.Lines(); len(l) != 1 || l[0].Stream != pluginsdk.Stderr {
		t.Fatalf("lines %v", l)
	}
	if p := e.Progresses(); len(p) != 1 || p[0].Pct != 0.5 {
		t.Fatalf("progress %v", p)
	}
	if got := e.Text(); got != "a\nhalf\n" {
		t.Fatalf("text %q", got)
	}
	s := plugintest.FakeSecrets{"secret://a": "v"}
	if v, err := s.Resolve("secret://a"); err != nil || v != "v" {
		t.Fatalf("resolve = %q %v", v, err)
	}
	if _, err := s.Resolve("secret://b"); err == nil {
		t.Fatal("missing secret should error")
	}
}

// leaky logs its secret; the contract suite must flag it.
type leaky struct{}

func (leaky) Manifest() pluginsdk.Manifest                    { return pluginsdk.Manifest{} }
func (leaky) Validate(context.Context, json.RawMessage) error { return nil }
func (leaky) Execute(_ context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
	v, _ := req.Secrets.Resolve("secret://k")
	emit.Log(pluginsdk.Stdout, "token="+v)
	return pluginsdk.Result{}, nil
}

func TestExecAndLeakDetection(t *testing.T) {
	c := plugintest.Case{Secrets: map[string]string{"secret://k": "hunter2"}}
	o := plugintest.Exec(context.Background(), t, leaky{}, c)
	if !strings.Contains(o.Emitter.Text(), "hunter2") {
		t.Fatal("fake emitter must not mask, so leaks are detectable")
	}
	if got := plugintest.LeakedSecrets(c.Secrets, o); len(got) != 1 || got[0] != "secret://k" {
		t.Fatalf("leaked = %v", got)
	}
	clean := plugintest.Outcome{Emitter: &plugintest.FakeEmitter{}}
	if got := plugintest.LeakedSecrets(c.Secrets, clean); len(got) != 0 {
		t.Fatalf("false positive: %v", got)
	}
}
