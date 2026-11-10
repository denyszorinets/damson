package plugintest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

type out struct {
	N int `json:"n"`
}

// fake is a configurable plugin for exercising the suite itself.
type fake struct {
	exec func(ctx context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error)
}

func (fake) Manifest() pluginsdk.Manifest {
	return pluginsdk.Manifest{Name: "fake", Version: "1.0.0", Title: "Fake",
		InputSchema: pluginsdk.MustSchemaFor[out](), OutputSchema: pluginsdk.MustSchemaFor[out](),
		TargetMode: pluginsdk.TargetNone}
}
func (fake) Validate(_ context.Context, in json.RawMessage) error {
	if !json.Valid(in) {
		return pluginsdk.ValidationError("bad json")
	}
	return nil
}
func (f fake) Execute(ctx context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
	return f.exec(ctx, req, emit)
}

func TestValidateAgainst(t *testing.T) {
	s := pluginsdk.MustSchemaFor[out]()
	tests := []struct {
		name, doc string
		wantErr   bool
	}{
		{"ok", `{"n":1}`, false},
		{"missing required", `{}`, true},
		{"wrong type", `{"n":"x"}`, true},
		{"additional property", `{"n":1,"extra":2}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateAgainst(s, []byte(tc.doc)); (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestExecRecoversPanicAndRecordsLogger(t *testing.T) {
	p := fake{exec: func(_ context.Context, req pluginsdk.ExecRequest, _ pluginsdk.Emitter) (pluginsdk.Result, error) {
		v, _ := req.Secrets.Resolve("secret://k")
		req.Logger.Info("token", "v", v)
		panic("boom")
	}}
	c := Case{Secrets: map[string]string{"secret://k": "hunter2"}}
	o := Exec(context.Background(), t, p, c)
	if se := pluginsdk.AsStepError(o.Err); se == nil || se.Kind != pluginsdk.KindInternal {
		t.Fatalf("panic not reported as internal: %v", o.Err)
	}
	if !strings.Contains(o.LogOutput, "hunter2") {
		t.Fatalf("logger output not recorded: %q", o.LogOutput)
	}
	if got := LeakedSecrets(c.allSecrets(), o); len(got) != 1 {
		t.Fatalf("logger leak not detected: %v", got)
	}
}

func TestBundleLeakDetection(t *testing.T) {
	c := Case{Bundle: map[string]map[string]string{"secret://ssh": {"private_key": "KEYDATA", "username": "bob"}}}
	p := fake{exec: func(_ context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
		f, err := req.Secrets.ResolveFields("secret://ssh")
		if err != nil {
			return pluginsdk.Result{}, err
		}
		emit.Log(pluginsdk.Stdout, "user "+f["username"])
		return pluginsdk.Result{}, nil
	}}
	o := Exec(context.Background(), t, p, c)
	got := LeakedSecrets(c.allSecrets(), o)
	if len(got) != 1 || got[0] != "secret://ssh#username" {
		t.Fatalf("leaked = %v", got)
	}
}

func TestExecBoundedDetectsIgnoredCancellation(t *testing.T) {
	release := make(chan struct{})
	p := fake{exec: func(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error) {
		<-release
		return pluginsdk.Result{}, nil
	}}
	_, ok := execBounded(context.Background(), t, p, Case{Input: json.RawMessage(`{}`)}, 20*time.Millisecond)
	close(release)
	if ok {
		t.Fatal("a plugin that ignores cancellation must be reported")
	}
}

func TestBlockingCase(t *testing.T) {
	p := fake{exec: func(ctx context.Context, req pluginsdk.ExecRequest, _ pluginsdk.Emitter) (pluginsdk.Result, error) {
		if err := ctx.Err(); err != nil {
			return pluginsdk.Result{Error: pluginsdk.AsStepError(err)}, nil
		}
		var in out
		_ = json.Unmarshal(req.Input, &in)
		if in.N != 99 {
			return pluginsdk.Result{Outputs: json.RawMessage(`{"n":1}`)}, nil
		}
		<-ctx.Done()
		return pluginsdk.Result{}, ctx.Err()
	}}
	RunContract(t, p, Options{
		Cases:    []Case{{Name: "c", Input: json.RawMessage(`{"n":1}`)}},
		Blocking: &Case{Name: "sleep", Input: json.RawMessage(`{"n":99}`)},
		Timeout:  time.Second,
	})
}

func TestCaseNodeProblems(t *testing.T) {
	m := pluginsdk.Manifest{TargetMode: pluginsdk.TargetNodes, NodePaths: []string{"/source/node/name", "/destination/node/name"}}
	two := map[string]pluginsdk.NodeInfo{"a": {Name: "a"}, "b": {Name: "b"}}
	in := json.RawMessage(`{"source":{"node":{"name":"a"}},"destination":{"node":{"name":"b"}}}`)
	for name, tc := range map[string]struct {
		c    Case
		want int
	}{
		"matches":      {Case{Name: "ok", Input: in, Nodes: two}, 0},
		"missing node": {Case{Name: "m", Input: in, Nodes: map[string]pluginsdk.NodeInfo{"a": {}}}, 1},
		"extra node":   {Case{Name: "e", Input: json.RawMessage(`{"source":{"s3":{}}}`), Nodes: two}, 1},
		"bad name":     {Case{Name: "b", Input: json.RawMessage(`{"source":{"node":{"name":3}}}`)}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			if got := caseNodeProblems(m, Options{Cases: []Case{tc.c}}); len(got) != tc.want {
				t.Fatalf("problems = %v; want %d", got, tc.want)
			}
		})
	}
}

func TestCaseNodeProblemsInventory(t *testing.T) {
	m := pluginsdk.Manifest{TargetMode: pluginsdk.TargetInventory, NodeListPath: "/hosts"}
	two := map[string]pluginsdk.NodeInfo{"a": {Name: "a"}, "b": {Name: "b"}}
	in := json.RawMessage(`{"hosts":["b","a"]}`)
	for name, tc := range map[string]struct {
		c    Case
		want int
	}{
		"matches":      {Case{Name: "ok", Input: in, Nodes: two}, 0},
		"missing host": {Case{Name: "m", Input: in, Nodes: map[string]pluginsdk.NodeInfo{"a": {}}}, 1},
		"extra host":   {Case{Name: "e", Input: json.RawMessage(`{"hosts":["a"]}`), Nodes: two}, 1},
		"no list":      {Case{Name: "n", Input: json.RawMessage(`{}`)}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			if got := caseNodeProblems(m, Options{Cases: []Case{tc.c}}); len(got) != tc.want {
				t.Fatalf("problems = %v; want %d", got, tc.want)
			}
		})
	}
}

// inventoryPlugin runs on the hosts it was given; failClosed makes it refuse a
// listed host that has no resolved node.
type inventoryPlugin struct{ failClosed bool }

func (inventoryPlugin) Manifest() pluginsdk.Manifest {
	return pluginsdk.Manifest{
		Name: "inv", Version: "1.0.0", Title: "inv", TargetMode: pluginsdk.TargetInventory, NodeListPath: "/hosts",
		InputSchema: pluginsdk.MustSchemaFor[struct {
			Hosts []string `json:"hosts"`
		}](), OutputSchema: pluginsdk.MustSchemaFor[struct{}](),
	}
}

func (inventoryPlugin) Validate(_ context.Context, in json.RawMessage) error {
	var v struct {
		Hosts []string `json:"hosts"`
	}
	if err := json.Unmarshal(in, &v); err != nil {
		return pluginsdk.ValidationError("bad input: %v", err)
	}
	return nil
}

func (p inventoryPlugin) Execute(ctx context.Context, req pluginsdk.ExecRequest, _ pluginsdk.Emitter) (pluginsdk.Result, error) {
	if err := ctx.Err(); err != nil {
		return pluginsdk.Result{}, err
	}
	var in struct {
		Hosts []string `json:"hosts"`
	}
	_ = json.Unmarshal(req.Input, &in)
	for _, h := range in.Hosts {
		if _, ok := req.Nodes[h]; !ok && p.failClosed {
			return pluginsdk.Result{Error: pluginsdk.ValidationError("node %q not resolved by the controller", h)}, nil
		}
	}
	return pluginsdk.Result{Outputs: json.RawMessage(`{}`)}, nil
}

func TestInventoryFailClosedCheck(t *testing.T) {
	opts := Options{Cases: []Case{{
		Name: "c", Input: json.RawMessage(`{"hosts":["a","b"]}`),
		Nodes: map[string]pluginsdk.NodeInfo{"a": {Name: "a"}, "b": {Name: "b"}},
	}}}
	t.Run("a plugin that fails closed passes the suite", func(t *testing.T) {
		RunContract(t, inventoryPlugin{failClosed: true}, opts)
	})
	t.Run("a plugin that runs anyway is caught", func(t *testing.T) {
		o := opts
		o.Timeout = time.Second
		if inventoryFailClosedProblem(t, inventoryPlugin{}, o) == "" {
			t.Fatal("the check did not catch a plugin that ignores a missing node")
		}
	})
}
