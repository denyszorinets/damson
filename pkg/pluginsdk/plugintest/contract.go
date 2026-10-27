package plugintest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
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
	// Secrets are single-value secrets made available through
	// ExecRequest.Secrets. The suite fails if any value appears in emitted
	// logs, progress, outputs, logger output or error text.
	Secrets map[string]string
	// Bundle holds whole-credential secrets (ref to fields), resolved with
	// ResolveFields. Its values are leak-checked like Secrets.
	Bundle map[string]map[string]string
}

// Options configures the contract suite for one plugin.
type Options struct {
	// Cases must contain at least one valid execution of the plugin.
	Cases []Case
	// Blocking is an optional execution that keeps running until cancelled
	// (for example a long sleep). The suite cancels it after a short delay and
	// requires Execute to return a cancelled error within Timeout.
	Blocking *Case
	// Timeout bounds each call; a plugin that ignores cancellation fails.
	// Zero means 5 seconds.
	Timeout time.Duration
}

// Outcome is the recorded result of one Exec call.
type Outcome struct {
	Result  pluginsdk.Result
	Err     error
	Emitter *FakeEmitter
	// LogOutput is everything the plugin wrote to req.Logger.
	LogOutput string
}

// Exec runs p.Execute for c with a fake emitter, a recording logger and a temp
// WorkDir. A panic in the plugin is recovered and reported as an internal
// error in Outcome.Err. Exec returns only after Execute has returned.
func Exec(ctx context.Context, t testing.TB, p pluginsdk.Plugin, c Case) Outcome {
	t.Helper()
	em := &FakeEmitter{}
	logs := &lockedBuffer{}
	req := pluginsdk.ExecRequest{
		Input:   c.Input,
		Secrets: resolverFor(c),
		Nodes:   c.Nodes,
		WorkDir: t.TempDir(),
		Logger:  slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	o := Outcome{Emitter: em}
	func() {
		defer func() {
			if r := recover(); r != nil {
				o.Err = pluginsdk.NewError(pluginsdk.KindInternal, fmt.Sprintf("panic: %v", r), nil)
			}
		}()
		o.Result, o.Err = p.Execute(ctx, req, em)
	}()
	o.LogOutput = logs.String()
	return o
}

// resolverFor merges a case's single-value secrets and credential bundle.
func resolverFor(c Case) pluginsdk.SecretResolver {
	b := FakeBundle{}
	for ref, v := range c.Secrets {
		b[ref] = map[string]string{"value": v}
	}
	for ref, fields := range c.Bundle {
		b[ref] = fields
	}
	return b
}

// RunContract runs the contract suite against p:
//
//   - manifest is valid and its checksum is computable
//   - input and output schemas marshal to JSON objects
//   - Validate rejects malformed input with a validation error
//   - Execute succeeds for every case, returns a JSON object (or no outputs)
//     and never leaks secret values
//   - Execute honors an already-cancelled context promptly, and, when
//     Options.Blocking is set, cancellation of a running execution
//   - Execute outputs conform to the manifest's OutputSchema, and every
//     case input conforms to its InputSchema
//   - Execute never leaks secret values (plaintext match only; encoded
//     variants are the agent masker's job, ARCHITECTURE.md section 9.4)
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
	if opts.Blocking != nil {
		t.Run("cancel_blocking", func(t *testing.T) { checkBlocking(t, p, opts) })
	}
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
	m := p.Manifest()
	for _, c := range opts.Cases {
		if err := p.Validate(ctx, c.Input); err != nil {
			t.Errorf("case %q: Validate rejected a valid input: %v", c.Name, err)
		}
		if err := validateAgainst(m.InputSchema, c.Input); err != nil {
			t.Errorf("case %q: input does not conform to InputSchema: %v", c.Name, err)
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
			checkOutputs(t, p.Manifest(), o.Result.Outputs)
			assertNoSecrets(t, c, o)
		})
	}
}

// checkOutputs requires outputs to be a JSON object conforming to OutputSchema.
// Absent outputs are checked as an empty object.
func checkOutputs(t *testing.T, m pluginsdk.Manifest, out json.RawMessage) {
	t.Helper()
	if len(out) == 0 {
		out = json.RawMessage(`{}`)
	}
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("outputs are not a JSON object: %v", err)
	}
	if err := validateAgainst(m.OutputSchema, out); err != nil {
		t.Errorf("outputs do not conform to OutputSchema: %v\noutputs: %s", err, out)
	}
}

func assertNoSecrets(t *testing.T, c Case, o Outcome) {
	t.Helper()
	for _, ref := range LeakedSecrets(c.allSecrets(), o) {
		t.Errorf("value of secret %q leaked into logs, outputs or errors", ref)
	}
}

// LeakedSecrets returns the refs whose values appear in o's logs, progress,
// logger output, outputs or errors. The match is plaintext only.
func LeakedSecrets(secrets map[string]string, o Outcome) []string {
	haystack := []byte(o.Emitter.Text() + o.LogOutput)
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
	o, ok := execBounded(ctx, t, p, c, opts.Timeout)
	if !ok {
		t.Fatalf("Execute did not return within %s of cancellation", opts.Timeout)
	}
	requireCancelled(t, c, o)
}

func checkBlocking(t *testing.T, p pluginsdk.Plugin, opts Options) {
	c := *opts.Blocking
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(blockingDelay, cancel)
	o, ok := execBounded(ctx, t, p, c, opts.Timeout+blockingDelay)
	if !ok {
		t.Fatalf("running Execute did not return within %s of cancellation", opts.Timeout)
	}
	requireCancelled(t, c, o)
}

// blockingDelay is how long a Blocking case runs before it is cancelled.
const blockingDelay = 50 * time.Millisecond

func requireCancelled(t *testing.T, c Case, o Outcome) {
	t.Helper()
	if o.Err == nil && o.Result.Error == nil {
		t.Fatal("Execute succeeded although its context was cancelled")
	}
	if se := pluginsdk.AsStepError(firstErr(o)); se.Kind != pluginsdk.KindCancelled {
		t.Fatalf("cancelled Execute reported kind %s, want cancelled", se.Kind)
	}
	assertNoSecrets(t, c, o)
}

// execBounded runs Exec in a goroutine and waits at most d for it. When the
// plugin does not return, ctx is expected to be cancelled by the caller (so a
// well-behaved goroutine exits) and the test is told via ok=false. The
// goroutine is joined on cleanup for a short grace period so a plugin that
// does return late does not outlive the test.
func execBounded(ctx context.Context, t *testing.T, p pluginsdk.Plugin, c Case, d time.Duration) (Outcome, bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan Outcome, 1)
	workDirT := &tempDirTB{TB: t, dir: t.TempDir()}
	go func() { done <- Exec(ctx, workDirT, p, c) }()
	select {
	case o := <-done:
		cancel()
		return o, true
	case <-time.After(d):
		cancel()
		select {
		case <-done:
		case <-time.After(d):
		}
		return Outcome{}, false
	}
}

// tempDirTB hands out a precreated directory so Exec never touches t from a
// goroutine after the test finished.
type tempDirTB struct {
	testing.TB
	dir string
}

func (b *tempDirTB) TempDir() string { return b.dir }

func firstErr(o Outcome) error {
	if o.Err != nil {
		return o.Err
	}
	return o.Result.Error
}

// allSecrets flattens Secrets and Bundle into ref to value pairs for leak
// checks. Bundle fields are keyed "ref#field".
func (c Case) allSecrets() map[string]string {
	out := make(map[string]string, len(c.Secrets))
	for ref, v := range c.Secrets {
		out[ref] = v
	}
	for ref, fields := range c.Bundle {
		for k, v := range fields {
			out[ref+"#"+k] = v
		}
	}
	return out
}

// lockedBuffer is a goroutine-safe bytes.Buffer for the recording logger.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
