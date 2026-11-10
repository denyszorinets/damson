package plugincli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invopop/jsonschema"

	"github.com/denyszorinets/damson/internal/plugincli"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/all"
)

type result struct {
	code        int
	stdout, err string
}

func run(t *testing.T, plugins []pluginsdk.Plugin, stdin string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	code := plugincli.Run(context.Background(), args, plugincli.Env{
		Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb, Plugins: plugins,
	})
	return result{code, out.String(), errb.String()}
}

func write(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUsage(t *testing.T) {
	if r := run(t, nil, ""); r.code != plugincli.ExitUsage || !strings.Contains(r.err, "Usage: damson plugin") {
		t.Fatalf("no args = %+v", r)
	}
	if r := run(t, nil, "", "help"); r.code != plugincli.ExitOK || !strings.Contains(r.stdout, "validate") {
		t.Fatalf("help = %+v", r)
	}
	if r := run(t, nil, "", "frobnicate"); r.code != plugincli.ExitUsage || !strings.Contains(r.err, "unknown command") {
		t.Fatalf("unknown = %+v", r)
	}
}

func TestNewScaffold(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p")
	r := run(t, nil, "", "new", "my-plugin", "--dir", dir, "--module", "example.com/me/my-plugin")
	if r.code != plugincli.ExitOK {
		t.Fatalf("new = %+v", r)
	}
	for _, f := range []string{"go.mod", "myplugin.go", "myplugin_test.go", "README.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
	src, _ := os.ReadFile(filepath.Join(dir, "myplugin.go"))
	for _, want := range []string{"package myplugin", `Name:         "my-plugin"`, `Title:        "My Plugin"`} {
		if !strings.Contains(string(src), want) {
			t.Errorf("myplugin.go lacks %q", want)
		}
	}
	// A second run into the same directory is refused and changes nothing.
	if r := run(t, nil, "", "new", "my-plugin", "--dir", dir); r.code != plugincli.ExitError || !strings.Contains(r.err, "not empty") {
		t.Fatalf("second new = %+v", r)
	}
}

func TestNewRejectsBadNames(t *testing.T) {
	d := t.TempDir()
	for name, args := range map[string][]string{
		"upper":      {"new", "MyPlugin", "--dir", filepath.Join(d, "a")},
		"digit":      {"new", "1plugin", "--dir", filepath.Join(d, "b")},
		"space":      {"new", "my plugin", "--dir", filepath.Join(d, "c")},
		"none":       {"new"},
		"two":        {"new", "a", "b"},
		"bad module": {"new", "ok", "--dir", filepath.Join(d, "e"), "--module", "bad module"},
		"dotdot":     {"new", "ok", "--dir", filepath.Join(d, "f"), "--module", "a/../b"},
	} {
		if r := run(t, nil, "", args...); r.code != plugincli.ExitUsage {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if entries, _ := os.ReadDir(d); len(entries) != 0 {
		t.Fatalf("a rejected name created %d entries", len(entries))
	}
}

func TestNewPackageNames(t *testing.T) {
	for name, pkg := range map[string]string{"func": "funcplugin", "go": "goplugin", "a-b_c": "abc", "plugin": "pluginplugin", "json": "jsonplugin", "context": "contextplugin", "testing": "testingplugin"} {
		dir := filepath.Join(t.TempDir(), "p")
		if r := run(t, nil, "", "new", name, "--dir", dir); r.code != plugincli.ExitOK {
			t.Fatalf("%s: %+v", name, r)
		}
		if _, err := os.Stat(filepath.Join(dir, pkg+".go")); err != nil {
			t.Errorf("%s: want %s.go: %v", name, pkg, err)
		}
	}
}

// The scaffold compiles and passes the contract suite and the source rules
// unchanged. It builds against this checkout of the SDK (a replace directive)
// and the module cache or proxy, so it needs the go tool.
func TestScaffoldPassesItsOwnTests(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a module")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("the go tool is not available")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "p")
	if r := run(t, nil, "", "new", "scaffolded", "--dir", dir, "--module", "example.com/scaffolded"); r.code != plugincli.ExitOK {
		t.Fatalf("new = %+v", r)
	}
	mod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	mod = append(mod, []byte("\nrequire github.com/denyszorinets/damson v0.0.0\n\nreplace github.com/denyszorinets/damson => "+repo+"\n")...)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), mod, 0o600); err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile(filepath.Join(repo, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), sum, 0o600); err != nil {
		t.Fatal(err)
	}
	// The scaffold needs the SDK's requirements; take them from the repo.
	repoMod, _ := os.ReadFile(filepath.Join(repo, "go.mod"))
	var req []string
	inBlock := false
	for _, l := range strings.Split(string(repoMod), "\n") {
		switch {
		case strings.HasPrefix(l, "require ("):
			inBlock = true
		case inBlock && l == ")":
			inBlock = false
		case inBlock && strings.TrimSpace(l) != "":
			req = append(req, l)
		}
	}
	mod = append(mod, []byte("\nrequire (\n"+strings.Join(req, "\n")+"\n)\n")...)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), mod, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(goBin, "test", "-count=1", "-v", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go test in the scaffold failed: %v\n%s", err, out)
	}
	for _, want := range []string{"--- PASS: TestContract", "--- PASS: TestSource", "--- PASS: TestExecute"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the scaffold's tests did not run %q:\n%s", want, out)
		}
	}
}

func TestValidateBuiltins(t *testing.T) {
	r := run(t, all.Plugins(), "", "validate")
	if r.code != plugincli.ExitOK || !strings.Contains(r.stdout, "ok    ssh@") || !strings.Contains(r.stdout, "ok    transfer@") {
		t.Fatalf("validate = %+v", r)
	}
	if r := run(t, all.Plugins(), "", "validate", "ssh"); r.code != plugincli.ExitOK || strings.Contains(r.stdout, "transfer") {
		t.Fatalf("validate ssh = %+v", r)
	}
	if r := run(t, all.Plugins(), "", "validate", "ssh@9"); r.code != plugincli.ExitUsage {
		t.Fatalf("validate ssh@9 = %+v", r)
	}
	if r := run(t, all.Plugins(), "", "validate", "nope"); r.code != plugincli.ExitUsage || !strings.Contains(r.err, "unknown plugin") {
		t.Fatalf("validate nope = %+v", r)
	}
	if r := run(t, nil, "", "validate", "a", "b"); r.code != plugincli.ExitUsage {
		t.Fatalf("two refs = %+v", r)
	}
}

// stub is a plugin with a settable manifest.
type stub struct {
	m    pluginsdk.Manifest
	exec func(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error)
}

func (s stub) Manifest() pluginsdk.Manifest                  { return s.m }
func (stub) Validate(context.Context, json.RawMessage) error { return nil }
func (s stub) Execute(ctx context.Context, r pluginsdk.ExecRequest, e pluginsdk.Emitter) (pluginsdk.Result, error) {
	return s.exec(ctx, r, e)
}

type empty struct{}

func goodManifest(name, version string) pluginsdk.Manifest {
	return pluginsdk.Manifest{
		Name: name, Version: version, Title: name,
		InputSchema: pluginsdk.MustSchemaFor[empty](), OutputSchema: pluginsdk.MustSchemaFor[empty](),
		TargetMode: pluginsdk.TargetNone,
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	bad := goodManifest("Bad_Name", "1.0")
	bad.Title = ""
	notObject := goodManifest("scalar", "1.0.0")
	notObject.OutputSchema = &jsonschema.Schema{Type: "string"}
	plugins := []pluginsdk.Plugin{
		stub{m: goodManifest("good", "1.0.0")},
		stub{m: goodManifest("good", "1.0.0")}, // listed twice
		stub{m: bad},
		stub{m: notObject},
	}
	r := run(t, plugins, "", "validate")
	if r.code != plugincli.ExitError {
		t.Fatalf("validate = %+v", r)
	}
	for _, want := range []string{"ok    good@1.0.0", "FAIL  good@1.0.0: listed twice", "FAIL  Bad_Name@1.0", "FAIL  scalar@1.0.0: output schema: must describe an object"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, r.stdout)
		}
	}
	if !strings.Contains(r.err, "3 of 4 plugins failed") {
		t.Errorf("stderr = %q", r.err)
	}
}

// leaky puts the secret into a log line, an output and an error, the way a
// careless plugin would.
func leaky() stub {
	m := goodManifest("leaky", "1.2.3")
	m.InputSchema = pluginsdk.MustSchemaFor[struct {
		Fail bool `json:"fail,omitempty"`
	}]()
	m.OutputSchema = pluginsdk.MustSchemaFor[struct {
		Token string `json:"token"`
		N     int    `json:"n"`
	}]()
	return stub{m: m, exec: func(_ context.Context, r pluginsdk.ExecRequest, e pluginsdk.Emitter) (pluginsdk.Result, error) {
		tok, err := r.Secrets.Resolve("secret://t/token")
		if err != nil {
			return pluginsdk.Result{Error: pluginsdk.NewError(pluginsdk.KindAuth, "no token", err)}, nil
		}
		e.Log(pluginsdk.Stdout, "token is "+tok)
		out, _ := json.Marshal(map[string]any{"token": tok, "n": 7})
		res := pluginsdk.Result{Outputs: out}
		var in struct{ Fail bool }
		_ = json.Unmarshal(r.Input, &in)
		if in.Fail {
			res.Error = pluginsdk.NewError(pluginsdk.KindExitCode, "failed with "+tok, nil)
		}
		return res, nil
	}}
}

const secretValue = "s3cr3t-VALUE-9f2a"

func TestRunMasksEverythingItPrints(t *testing.T) {
	d := t.TempDir()
	secrets := write(t, d, "secrets.json", `{"secret://t/token": "`+secretValue+`"}`, 0o600)
	for name, tc := range map[string]struct {
		input string
		code  int
	}{"ok": {`{}`, plugincli.ExitOK}, "failing": {`{"fail":true}`, plugincli.ExitError}} {
		t.Run(name, func(t *testing.T) {
			in := write(t, d, name+".json", tc.input, 0o600)
			r := run(t, []pluginsdk.Plugin{leaky()}, "", "run", "leaky", "--input", in, "--secrets", secrets)
			if r.code != tc.code {
				t.Fatalf("run = %+v", r)
			}
			if strings.Contains(r.stdout, secretValue) || strings.Contains(r.err, secretValue) {
				t.Fatalf("the secret was printed:\nstdout: %s\nstderr: %s", r.stdout, r.err)
			}
			if !strings.Contains(r.err, "[stdout] token is ***") {
				t.Errorf("log line not masked: %q", r.err)
			}
			var out struct {
				OK       bool           `json:"ok"`
				Outputs  map[string]any `json:"outputs"`
				Redacted []string       `json:"redacted"`
				Error    *struct {
					Kind, Message string
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
				t.Fatalf("stdout is not JSON: %v\n%s", err, r.stdout)
			}
			if out.Outputs["token"] != "***" || out.Outputs["n"] != float64(7) || len(out.Redacted) != 1 {
				t.Errorf("outputs = %+v", out)
			}
			if tc.code == plugincli.ExitError && (out.Error == nil || out.Error.Kind != "exit_code" || out.Error.Message != "failed with ***") {
				t.Errorf("error = %+v", out.Error)
			}
		})
	}
}

func TestRunInputFromStdinAndRealPlugin(t *testing.T) {
	// transfer validates the input against its schema before it runs.
	r := run(t, all.Plugins(), `{"mode":"copy"}`, "run", "transfer", "--input", "-")
	if r.code != plugincli.ExitError || !strings.Contains(r.stdout, `"kind": "validation"`) {
		t.Fatalf("run = %+v", r)
	}
	if strings.Contains(r.stdout, "panic") {
		t.Fatalf("stdout = %s", r.stdout)
	}
}

func TestRunRejectsBadFilesAndArgs(t *testing.T) {
	d := t.TempDir()
	p := []pluginsdk.Plugin{leaky()}
	loose := write(t, d, "loose.json", `{"secret://t/token": "x"}`, 0o644)
	notRef := write(t, d, "noref.json", `{"token": "x"}`, 0o600)
	badShape := write(t, d, "shape.json", `{"secret://t/token": 5}`, 0o600)
	frag := write(t, d, "frag.json", `{"secret://t/token#field": "x"}`, 0o600)
	notJSON := write(t, d, "bad.json", `secret-in-a-syntax-error-`+secretValue, 0o600)
	input := write(t, d, "in.json", `[1]`, 0o600)
	nodes := write(t, d, "nodes.json", `{"a": {"name": "b"}}`, 0o600)
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no ref":         {[]string{"run"}, "exactly one plugin"},
		"unknown":        {[]string{"run", "nope"}, "unknown plugin"},
		"loose mode":     {[]string{"run", "leaky", "--secrets", loose}, "0600 or stricter"},
		"not a ref":      {[]string{"run", "leaky", "--secrets", notRef}, "secret:// ref"},
		"bad shape":      {[]string{"run", "leaky", "--secrets", badShape}, "string or an object"},
		"ref with field": {[]string{"run", "leaky", "--secrets", frag}, "without a #field"},
		"not json":       {[]string{"run", "leaky", "--secrets", notJSON}, "not a JSON object"},
		"missing file":   {[]string{"run", "leaky", "--secrets", filepath.Join(d, "none")}, "opening the secrets file"},
		"input array":    {[]string{"run", "leaky", "--input", input}, "JSON object"},
		"node mismatch":  {[]string{"run", "leaky", "--nodes", nodes}, "is named"},
		"bad config":     {[]string{"run", "leaky", "--config", "novalue"}, "want k=v"},
	} {
		t.Run(name, func(t *testing.T) {
			r := run(t, p, "", tc.args...)
			if r.code != plugincli.ExitUsage || !strings.Contains(r.err, tc.want) {
				t.Fatalf("run = %+v; want usage error containing %q", r, tc.want)
			}
			if strings.Contains(r.err+r.stdout, secretValue) {
				t.Fatal("a secret leaked into an error message")
			}
		})
	}
}

func TestRunPicksTheHighestVersionAndHonorsPins(t *testing.T) {
	mk := func(v string) stub {
		return stub{m: goodManifest("multi", v), exec: func(_ context.Context, _ pluginsdk.ExecRequest, e pluginsdk.Emitter) (pluginsdk.Result, error) {
			e.Log(pluginsdk.Stdout, "version "+v)
			return pluginsdk.Result{}, nil
		}}
	}
	ps := []pluginsdk.Plugin{mk("1.4.0"), mk("2.1.0"), mk("2.0.0")}
	if r := run(t, ps, "", "run", "multi"); !strings.Contains(r.err, "version 2.1.0") {
		t.Fatalf("unpinned = %+v", r)
	}
	if r := run(t, ps, "", "run", "multi@1"); !strings.Contains(r.err, "version 1.4.0") {
		t.Fatalf("pinned = %+v", r)
	}
}

func TestRunPassesNodesConfigAndPanics(t *testing.T) {
	d := t.TempDir()
	nodes := write(t, d, "nodes.json", `{"web-1": {"address": "10.0.0.1", "port": 22}}`, 0o600)
	m := goodManifest("probe", "0.1.0")
	var got pluginsdk.ExecRequest
	probe := stub{m: m, exec: func(_ context.Context, r pluginsdk.ExecRequest, _ pluginsdk.Emitter) (pluginsdk.Result, error) {
		got = r
		if r.HostKeys != nil {
			r.HostKeys.ReportHostKey("web-1", "ssh-ed25519 AAAA")
		}
		return pluginsdk.Result{}, nil
	}}
	r := run(t, []pluginsdk.Plugin{probe}, "", "run", "probe", "--nodes", nodes, "--config", "allow_insecure_host_keys=true")
	if r.code != plugincli.ExitOK {
		t.Fatalf("run = %+v", r)
	}
	if n := got.Nodes["web-1"]; n.Name != "web-1" || n.Address != "10.0.0.1" || got.Config["allow_insecure_host_keys"] != "true" {
		t.Fatalf("request = %+v", got)
	}
	if !strings.Contains(r.err, "host key observed for web-1: ssh-ed25519 AAAA") {
		t.Errorf("stderr = %q", r.err)
	}
	if got.WorkDir == "" {
		t.Error("no work directory")
	} else if _, err := os.Stat(got.WorkDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("work directory not removed: %v", err)
	}

	boom := stub{m: goodManifest("boom", "0.1.0"), exec: func(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error) {
		panic("kaboom")
	}}
	if r := run(t, []pluginsdk.Plugin{boom}, "", "run", "boom"); r.code != plugincli.ExitError || !strings.Contains(r.stdout, "panicked") {
		t.Fatalf("panic = %+v", r)
	}
}

func TestRunTimeoutCancelsThePlugin(t *testing.T) {
	slow := stub{m: goodManifest("slow", "0.1.0"), exec: func(ctx context.Context, _ pluginsdk.ExecRequest, _ pluginsdk.Emitter) (pluginsdk.Result, error) {
		<-ctx.Done()
		return pluginsdk.Result{Error: pluginsdk.AsStepError(ctx.Err())}, nil
	}}
	r := run(t, []pluginsdk.Plugin{slow}, "", "run", "slow", "--timeout", "100ms")
	if r.code != plugincli.ExitError || !strings.Contains(r.stdout, `"kind": "timeout"`) {
		t.Fatalf("run = %+v", r)
	}
}

func TestRunPanicAndLoggerNeverPrintTheSecret(t *testing.T) {
	d := t.TempDir()
	secrets := write(t, d, "secrets.json", `{"secret://t/token": "`+secretValue+`"}`, 0o600)
	boom := stub{m: goodManifest("boom", "0.1.0"), exec: func(_ context.Context, r pluginsdk.ExecRequest, _ pluginsdk.Emitter) (pluginsdk.Result, error) {
		tok, _ := r.Secrets.Resolve("secret://t/token")
		r.Logger.Info("using " + tok)
		panic(fmt.Sprintf("%x", tok))
	}}
	r := run(t, []pluginsdk.Plugin{boom}, "", "run", "boom", "--secrets", secrets)
	if r.code != plugincli.ExitError || !strings.Contains(r.stdout, "panicked (string)") {
		t.Fatalf("run = %+v", r)
	}
	hexed := fmt.Sprintf("%x", secretValue)
	for _, out := range []string{r.stdout, r.err} {
		if strings.Contains(out, secretValue) || strings.Contains(out, hexed) {
			t.Fatalf("secret printed: %s", out)
		}
	}
	if !strings.Contains(r.err, "using ***") {
		t.Errorf("plugin logger output missing or unmasked: %q", r.err)
	}
}

func TestRunReportsAPluginThatIgnoresCancellation(t *testing.T) {
	defer plugincli.SetGraceForTest(50 * time.Millisecond)()
	release := make(chan struct{})
	defer close(release)
	stuck := stub{m: goodManifest("stuck", "0.1.0"), exec: func(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error) {
		<-release
		return pluginsdk.Result{}, nil
	}}
	r := run(t, []pluginsdk.Plugin{stuck}, "", "run", "stuck", "--timeout", "50ms")
	if r.code != plugincli.ExitError || !strings.Contains(r.stdout, "ignored cancellation") {
		t.Fatalf("run = %+v", r)
	}
}

func TestDoubleDashEndsFlags(t *testing.T) {
	r := run(t, []pluginsdk.Plugin{leaky()}, "", "run", "leaky", "--", "-a", "-b")
	if r.code != plugincli.ExitUsage {
		t.Fatalf("run = %+v", r)
	}
}
