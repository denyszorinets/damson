package plugintest

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Case is one valid execution used by the contract suite.
type Case struct {
	Name  string
	Input json.RawMessage
	// Nodes are passed to the plugin as ExecRequest.Nodes.
	Nodes map[string]pluginsdk.NodeInfo
	// Secrets are made available through ExecRequest.Secrets. The suite fails
	// if any value appears in emitted logs, progress, outputs or error text.
	Secrets map[string]string
}

// Options configures the contract suite for one plugin.
type Options struct {
	// Cases must contain at least one valid execution of the plugin.
	Cases []Case
	// Timeout bounds each call; a plugin that ignores cancellation fails.
	// Zero means 5 seconds.
	Timeout time.Duration
}

// Outcome is the recorded result of one Exec call.
type Outcome struct {
	Result  pluginsdk.Result
	Err     error
	Emitter *FakeEmitter
}

// Exec runs p.Execute for c with a fake emitter and a temp WorkDir.
func Exec(ctx context.Context, t testing.TB, p pluginsdk.Plugin, c Case) Outcome {
	t.Helper()
	em := &FakeEmitter{}
	res, err := p.Execute(ctx, pluginsdk.ExecRequest{
		Input:   c.Input,
		Secrets: FakeSecrets(c.Secrets),
		Nodes:   c.Nodes,
		WorkDir: t.TempDir(),
		Logger:  slog.New(slog.DiscardHandler),
	}, em)
	return Outcome{Result: res, Err: err, Emitter: em}
}

// RunContract runs the contract suite against p:
//
//   - manifest is valid and its checksum is computable
//   - input and output schemas marshal to JSON objects
//   - Validate rejects malformed input with a validation error
//   - Execute succeeds for every case, returns a JSON object (or no outputs)
//     and never leaks secret values
//   - Execute honors an already-cancelled context promptly
func RunContract(t *testing.T, p pluginsdk.Plugin, opts Options) {
	t.Helper()
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Second
	}
	m := p.Manifest()
	t.Run("manifest", func(t *testing.T) { checkManifest(t, m) })
	t.Run("validate", func(t *testing.T) { checkValidate(t, p, opts) })
	t.Run("execute", func(t *testing.T) { checkExecute(t, p, opts) })
	t.Run("cancellation", func(t *testing.T) { checkCancel(t, p, opts) })
}

// RunContractAll runs RunContract for every plugin, looking options up by
// manifest name. A plugin without options fails: every plugin needs a case.
func RunContractAll(t *testing.T, plugins []pluginsdk.Plugin, opts map[string]Options) {
	t.Helper()
	seen := map[string]bool{}
	for _, p := range plugins {
		m := p.Manifest()
		key := m.Name + "@" + m.Version
		if seen[key] {
			t.Errorf("duplicate plugin %s", key)
		}
		seen[key] = true
		t.Run(m.Name, func(t *testing.T) {
			o, ok := opts[m.Name]
			if !ok {
				t.Fatalf("no contract options for plugin %q", m.Name)
			}
			RunContract(t, p, o)
		})
	}
}

func checkManifest(t *testing.T, m pluginsdk.Manifest) {
	if err := m.Validate(); err != nil {
		t.Fatalf("invalid manifest: %v", err)
	}
	if _, err := m.Checksum(); err != nil {
		t.Fatalf("checksum: %v", err)
	}
	for name, s := range map[string]any{"input": m.InputSchema, "output": m.OutputSchema} {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("%s schema does not marshal: %v", name, err)
		}
		var obj map[string]any
		if err := json.Unmarshal(b, &obj); err != nil || len(obj) == 0 {
			t.Fatalf("%s schema is not a non-empty JSON object: %s", name, b)
		}
	}
}

func checkValidate(t *testing.T, p pluginsdk.Plugin, opts Options) {
	ctx := context.Background()
	err := p.Validate(ctx, json.RawMessage(`{`))
	if err == nil {
		t.Fatal("Validate accepted malformed JSON")
	}
	if se := pluginsdk.AsStepError(err); se.Kind != pluginsdk.KindValidation {
		t.Fatalf("Validate error kind = %s, want validation (%v)", se.Kind, err)
	}
	for _, c := range opts.Cases {
		if err := p.Validate(ctx, c.Input); err != nil {
			t.Errorf("case %q: Validate rejected a valid input: %v", c.Name, err)
		}
	}
}

func checkExecute(t *testing.T, p pluginsdk.Plugin, opts Options) {
	if len(opts.Cases) == 0 {
		t.Fatal("no cases: provide at least one valid execution")
	}
	for _, c := range opts.Cases {
		t.Run(c.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
			defer cancel()
			o := Exec(ctx, t, p, c)
			if o.Err != nil {
				t.Fatalf("Execute error: %v", o.Err)
			}
			if o.Result.Error != nil {
				t.Fatalf("step error: %v", o.Result.Error)
			}
			if len(o.Result.Outputs) > 0 {
				var obj map[string]any
				if err := json.Unmarshal(o.Result.Outputs, &obj); err != nil {
					t.Fatalf("outputs are not a JSON object: %v", err)
				}
			}
			assertNoSecrets(t, c.Secrets, o)
		})
	}
}

func assertNoSecrets(t *testing.T, secrets map[string]string, o Outcome) {
	t.Helper()
	for _, ref := range LeakedSecrets(secrets, o) {
		t.Errorf("value of secret %q leaked into logs, outputs or errors", ref)
	}
}

// LeakedSecrets returns the refs whose values appear in o's logs, progress,
// outputs or errors.
func LeakedSecrets(secrets map[string]string, o Outcome) []string {
	haystack := []byte(o.Emitter.Text())
	haystack = append(haystack, o.Result.Outputs...)
	if o.Err != nil {
		haystack = append(haystack, o.Err.Error()...)
	}
	if o.Result.Error != nil {
		haystack = append(haystack, o.Result.Error.Error()...)
	}
	var leaked []string
	for ref, v := range secrets {
		if v != "" && bytes.Contains(haystack, []byte(v)) {
			leaked = append(leaked, ref)
		}
	}
	sort.Strings(leaked)
	return leaked
}

func checkCancel(t *testing.T, p pluginsdk.Plugin, opts Options) {
	if len(opts.Cases) == 0 {
		t.Fatal("no cases: provide at least one valid execution")
	}
	c := opts.Cases[0]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan Outcome, 1)
	go func() { done <- Exec(ctx, t, p, c) }()
	select {
	case o := <-done:
		failed := o.Err != nil || o.Result.Error != nil
		if !failed {
			t.Fatal("Execute succeeded with an already-cancelled context")
		}
		if se := pluginsdk.AsStepError(firstErr(o)); se.Kind != pluginsdk.KindCancelled {
			t.Fatalf("cancelled Execute reported kind %s, want cancelled", se.Kind)
		}
		assertNoSecrets(t, c.Secrets, o)
	case <-time.After(opts.Timeout):
		t.Fatalf("Execute did not return within %s of cancellation", opts.Timeout)
	}
}

func firstErr(o Outcome) error {
	if o.Err != nil {
		return o.Err
	}
	return o.Result.Error
}
