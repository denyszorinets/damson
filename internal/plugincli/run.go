package plugincli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/denyszorinets/damson/internal/agent"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// maxSecretsFile bounds the secrets file.
const maxSecretsFile = 1 << 20

// runOutput is what "plugin run" prints on stdout.
type runOutput struct {
	Plugin   string               `json:"plugin"`
	OK       bool                 `json:"ok"`
	Outputs  map[string]any       `json:"outputs,omitempty"`
	Redacted []string             `json:"redacted,omitempty"`
	Error    *pluginsdk.StepError `json:"error,omitempty"`
}

// runRun runs one linked plugin the way an agent would, without a controller:
// the input is validated against the manifest, the secrets are served from
// memory, and everything that is printed (logs, outputs, errors) is masked by
// the agent's own masker.
func runRun(ctx context.Context, args []string, env Env) int {
	fs := newFlags("run", env)
	input := fs.String("input", "", "JSON input file (- for stdin)")
	secretsFile := fs.String("secrets", "", "JSON secrets file")
	nodesFile := fs.String("nodes", "", "JSON nodes file")
	timeout := fs.Duration("timeout", 0, "stop the plugin after this long")
	config := map[string]string{}
	fs.Func("config", "plugin setting k=v (repeatable)", func(s string) error {
		k, v, ok := strings.Cut(s, "=")
		if !ok || k == "" {
			return errors.New("want k=v")
		}
		config[k] = v
		return nil
	})
	pos, err := parse(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(pos) != 1 {
		fmt.Fprintln(env.Stderr, "damson plugin run: exactly one plugin reference is required")
		return ExitUsage
	}
	plug, m, err := resolve(env.Plugins, pos[0])
	if err != nil {
		fmt.Fprintf(env.Stderr, "damson plugin run: %v\n", err)
		return ExitUsage
	}
	raw, err := readInput(*input, env.Stdin)
	if err != nil {
		fmt.Fprintf(env.Stderr, "damson plugin run: %v\n", err)
		return ExitUsage
	}
	bundle, err := readSecrets(*secretsFile)
	if err != nil {
		fmt.Fprintf(env.Stderr, "damson plugin run: %v\n", err)
		return ExitUsage
	}
	nodes, err := readNodes(*nodesFile)
	if err != nil {
		fmt.Fprintf(env.Stderr, "damson plugin run: %v\n", err)
		return ExitUsage
	}

	lm := agent.NewLocalMasking(bundle)
	defer lm.Wipe()
	out := &runOutput{Plugin: m.Name + "@" + m.Version}
	em := &emitter{w: env.Stderr, mask: lm.Mask}
	if lm.ShortSecrets() {
		em.system("a secret value shorter than 4 bytes is not masked in text")
	}

	res := execute(ctx, plug, m, raw, lm, nodes, config, *timeout, em)
	if res.Error != nil {
		out.Error = &pluginsdk.StepError{Kind: res.Error.Kind, Message: lm.Mask(res.Error.Message), ExitCode: res.Error.ExitCode}
	}
	outputs, redacted, oerr := lm.Outputs(res.Outputs)
	if oerr != nil && out.Error == nil {
		out.Error = &pluginsdk.StepError{Kind: pluginsdk.KindInternal, Message: lm.Mask(oerr.Error())}
	}
	out.Outputs, out.Redacted, out.OK = outputs, redacted, out.Error == nil
	enc := json.NewEncoder(env.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
	if !out.OK {
		return ExitError
	}
	return ExitOK
}

// resolve finds the plugin for a reference (name or name@major), the highest
// version when unpinned.
func resolve(plugins []pluginsdk.Plugin, ref string) (pluginsdk.Plugin, pluginsdk.Manifest, error) {
	manifests := make([]pluginsdk.Manifest, 0, len(plugins))
	for _, p := range plugins {
		manifests = append(manifests, p.Manifest())
	}
	reg, err := pluginreg.New(manifests, slog.New(slog.DiscardHandler))
	if err != nil {
		return nil, pluginsdk.Manifest{}, err
	}
	m, err := reg.Lookup(ref)
	if err != nil {
		return nil, m, fmt.Errorf("%w (linked plugins: %s)", err, names(plugins))
	}
	for _, p := range plugins {
		if pm := p.Manifest(); pm.Name == m.Name && pm.Version == m.Version {
			return p, m, nil
		}
	}
	return nil, m, fmt.Errorf("unknown plugin %q", ref)
}

func names(plugins []pluginsdk.Plugin) string {
	var ns []string
	for _, p := range plugins {
		ns = append(ns, p.Manifest().Name)
	}
	if len(ns) == 0 {
		return "none"
	}
	return strings.Join(ns, ", ")
}

// selectPlugins filters plugins by a reference for validate.
func selectPlugins(plugins []pluginsdk.Plugin, ref string) ([]pluginsdk.Plugin, error) {
	r, err := pluginreg.ParseRef(ref)
	if err != nil {
		return nil, err
	}
	var out []pluginsdk.Plugin
	for _, p := range plugins {
		m := p.Manifest()
		if m.Name != r.Name {
			continue
		}
		if r.Pinned {
			major, _, _ := strings.Cut(m.Version, ".")
			if n, err := strconv.ParseUint(major, 10, 64); err != nil || n != r.Major {
				continue
			}
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("unknown plugin %q (linked plugins: %s)", ref, names(plugins))
	}
	return out, nil
}

func readInput(path string, stdin io.Reader) (json.RawMessage, error) {
	var b []byte
	var err error
	switch path {
	case "":
		return json.RawMessage(`{}`), nil
	case "-":
		b, err = io.ReadAll(io.LimitReader(stdin, pluginreg.MaxInputBytes+1))
	default:
		b, err = readFileLimited(path, pluginreg.MaxInputBytes)
	}
	if err != nil {
		return nil, fmt.Errorf("reading the input: %w", err)
	}
	if len(b) > pluginreg.MaxInputBytes {
		return nil, fmt.Errorf("the input exceeds %d bytes", pluginreg.MaxInputBytes)
	}
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' || !json.Valid(b) {
		return nil, errors.New("the input must be a JSON object")
	}
	return b, nil
}

func readFileLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return b, nil
}

// readNodes reads {"name": {node fields}}; the key is the node name.
func readNodes(path string) (map[string]pluginsdk.NodeInfo, error) {
	if path == "" {
		return nil, nil
	}
	b, err := readFileLimited(path, maxSecretsFile)
	if err != nil {
		return nil, fmt.Errorf("reading the nodes file: %w", err)
	}
	var nodes map[string]pluginsdk.NodeInfo
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&nodes); err != nil {
		return nil, fmt.Errorf("the nodes file is not a JSON object of nodes: %w", err)
	}
	for name, n := range nodes {
		if n.Name == "" {
			n.Name = name
		}
		if n.Name != name {
			return nil, fmt.Errorf("node %q is named %q inside", name, n.Name)
		}
		nodes[name] = n
	}
	return nodes, nil
}

// emitter prints the plugin's log lines to stderr, masked.
type emitter struct {
	mu   sync.Mutex
	w    io.Writer
	mask func(string) string
}

func (e *emitter) Log(stream pluginsdk.Stream, line string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, _ = fmt.Fprintf(e.w, "[%s] %s\n", stream, e.mask(line))
}

func (e *emitter) Progress(pct float64, msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, _ = fmt.Fprintf(e.w, "[progress] %.0f%% %s\n", pct, e.mask(msg))
}

// Write lets the plugin's logger share the emitter's lock and writer; the
// logger masks its own records.
func (e *emitter) Write(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.w.Write(p)
}

func (e *emitter) system(msg string) { e.Log(pluginsdk.System, msg) }

// hostKeys prints the host keys a plugin observed; they are public data.
type hostKeys struct{ em *emitter }

func (h hostKeys) ReportHostKey(node, key string) {
	h.em.system("host key observed for " + node + ": " + key)
}

// graceAfterCancel is how long a plugin gets to return after its context ends.
var graceAfterCancel = 5 * time.Second

// execute validates the input and runs the plugin, recovering from a panic.
// A panic is reported by type only: its value can carry a secret.
func execute(ctx context.Context, p pluginsdk.Plugin, m pluginsdk.Manifest, raw json.RawMessage,
	lm *agent.LocalMasking, nodes map[string]pluginsdk.NodeInfo, config map[string]string,
	timeout time.Duration, em *emitter) pluginsdk.Result {
	if err := pluginreg.ValidateInput(m, raw); err != nil {
		return pluginsdk.Result{Error: pluginsdk.AsStepError(pluginsdk.ValidationError("%v", err))}
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	done := make(chan pluginsdk.Result, 1)
	go func() { done <- runGuarded(ctx, p, raw, lm, nodes, config, em) }()
	select {
	case res := <-done:
		return res
	case <-ctx.Done():
	}
	select {
	case res := <-done:
		return res
	case <-time.After(graceAfterCancel):
		return pluginsdk.Result{Error: pluginsdk.NewError(pluginsdk.KindInternal,
			"the plugin ignored cancellation (it must return when its context ends)", nil)}
	}
}

// runGuarded runs Validate and Execute and turns a panic into an error.
func runGuarded(ctx context.Context, p pluginsdk.Plugin, raw json.RawMessage, lm *agent.LocalMasking,
	nodes map[string]pluginsdk.NodeInfo, config map[string]string, em *emitter) (res pluginsdk.Result) {
	defer func() {
		if r := recover(); r != nil {
			// The panic value is not printed; the stack shows arguments as words only.
			fmt.Fprintf(em, "panic of type %T\n%s\n", r, debug.Stack())
			res = pluginsdk.Result{Error: pluginsdk.NewError(pluginsdk.KindInternal,
				fmt.Sprintf("the plugin panicked (%T)", r), nil)}
		}
	}()
	if err := p.Validate(ctx, raw); err != nil {
		return pluginsdk.Result{Error: pluginsdk.AsStepError(err)}
	}
	work, err := os.MkdirTemp("", "damson-plugin-run-")
	if err != nil {
		return pluginsdk.Result{Error: pluginsdk.NewError(pluginsdk.KindInternal, "creating a work directory failed", err)}
	}
	defer func() { _ = os.RemoveAll(work) }()
	res, err = p.Execute(ctx, pluginsdk.ExecRequest{
		Input: raw, Secrets: lm.Resolver(), Nodes: nodes, WorkDir: work,
		Logger: lm.Logger(slog.NewTextHandler(em, nil)), HostKeys: hostKeys{em}, Config: config,
	}, em)
	if err != nil && res.Error == nil {
		res.Error = pluginsdk.AsStepError(err)
	}
	return res
}
