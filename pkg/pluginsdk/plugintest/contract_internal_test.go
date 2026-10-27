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
