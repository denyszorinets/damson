// Package ansible is the built-in ansible plugin: it clones a repository and
// runs one playbook over a list of inventory nodes (ARCHITECTURE.md section
// 6.4, ADR-0055). It uses only pkg/pluginsdk and plugins/internal/*.
package ansible

import (
	"context"
	_ "embed" // callback plugin
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/internal/gitcheckout"
	"github.com/denyszorinets/damson/plugins/internal/proc"
)

//go:embed damson_events.py
var callbackSource []byte

// Plugins returns the plugins in this package.
func Plugins() []pluginsdk.Plugin {
	return []pluginsdk.Plugin{plugin{manifest: pluginsdk.Manifest{
		Name:         "ansible",
		Version:      "0.1.0",
		Title:        "Ansible",
		Description:  "Runs an Ansible playbook from a git repository over inventory nodes.",
		InputSchema:  pluginsdk.MustSchemaFor[Input](),
		OutputSchema: pluginsdk.MustSchemaFor[Output](),
		Idempotent:   false,
		Requires:     []string{"bin:ansible-playbook", "bin:git"},
		TargetMode:   pluginsdk.TargetInventory,
		NodeListPath: "/hosts",
	}}}
}

type plugin struct{ manifest pluginsdk.Manifest }

func (p plugin) Manifest() pluginsdk.Manifest { return p.manifest }

func (plugin) Validate(_ context.Context, input json.RawMessage) error {
	_, err := decode(input)
	return err
}

func (plugin) Execute(ctx context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
	res, err := execute(ctx, req, emit)
	if err != nil {
		res.Error = pluginsdk.AsStepError(err)
	}
	return res, nil
}

func execute(ctx context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
	if err := ctx.Err(); err != nil {
		return pluginsdk.Result{}, err
	}
	in, err := decode(req.Input)
	if err != nil {
		return pluginsdk.Result{}, err
	}
	if req.WorkDir == "" {
		return pluginsdk.Result{}, pluginsdk.NewError(pluginsdk.KindInternal, "no work directory", nil)
	}
	creds := filepath.Join(req.WorkDir, "creds")
	if err := os.MkdirAll(creds, 0o700); err != nil {
		return pluginsdk.Result{}, pluginsdk.NewError(pluginsdk.KindInternal, "creating the credentials directory failed", err)
	}
	defer func() { _ = os.RemoveAll(creds) }()

	// The inventory is built before anything is cloned: a missing node or
	// credential fails the step without touching the network.
	pl, err := writeInventory(creds, in, req)
	if err != nil {
		return pluginsdk.Result{}, err
	}
	if len(in.Hosts) == 0 {
		emit.Log(pluginsdk.System, "no hosts: nothing to run")
		return marshalResult(runOutcome{}, "", 0)
	}
	extra, err := writeExtraVars(creds, in, req)
	if err != nil {
		return pluginsdk.Result{}, err
	}
	vault, err := writeVault(creds, in, req)
	if err != nil {
		return pluginsdk.Result{}, err
	}

	repoDir := filepath.Join(req.WorkDir, "repo")
	co, err := checkout(ctx, repoDir, in.Repo, req, emit)
	if err != nil {
		return pluginsdk.Result{}, err
	}
	emit.Log(pluginsdk.System, "checked out "+co.Commit)

	rr, runErr := runPlaybook(ctx, req.WorkDir, repoDir, in, pl, extra, vault, emit)
	reportHostKeys(req, pl)
	res, merr := marshalResult(rr, co.Commit, rr.exitCode)
	if merr != nil {
		return res, merr
	}
	if runErr != nil {
		return res, runErr
	}
	return res, outcomeError(rr)
}

// checkout clones the repository of the step.
func checkout(ctx context.Context, dir string, r Repo, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (gitcheckout.Result, error) {
	return gitcheckout.CloneFor(ctx, dir, gitcheckout.Spec{URL: r.URL, Ref: r.Ref, Credential: r.Credential, Submodules: r.Submodules}, req,
		&proc.LineWriter{Emit: emit, Stream: pluginsdk.System})
}

// ConfigAllowInsecureGit is the Config key that also permits http, git and
// file repository URLs (agent.allow_insecure_git).
const ConfigAllowInsecureGit = "allow_insecure_git"

// writeExtraVars writes extra_vars with secret:// strings resolved, to a
// 0600 file; the values never reach the command line.
func writeExtraVars(dir string, in Input, req pluginsdk.ExecRequest) (string, error) {
	if len(in.ExtraVars) == 0 {
		return "", nil
	}
	resolved, err := resolveValue(in.ExtraVars, req.Secrets)
	if err != nil {
		return "", err
	}
	// Every string is marked unsafe, so ansible never templates it: a value
	// that came from a run parameter or a secret cannot run a lookup on the
	// agent. The playbook does its own templating.
	raw, err := json.Marshal(markUnsafe(resolved))
	if err != nil {
		return "", pluginsdk.ValidationError("extra_vars cannot be encoded")
	}
	path := filepath.Join(dir, "extra_vars.json")
	return path, writeSecret(path, raw)
}

// markUnsafe wraps every string as {"__ansible_unsafe": s}, which ansible's JSON
// loader reads as an unsafe (never templated) string.
func markUnsafe(v any) any {
	switch x := v.(type) {
	case string:
		return map[string]any{"__ansible_unsafe": x}
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = markUnsafe(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = markUnsafe(e)
		}
		return out
	}
	return v
}

func resolveValue(v any, secrets pluginsdk.SecretResolver) (any, error) {
	switch x := v.(type) {
	case string:
		if !strings.HasPrefix(x, "secret://") {
			return x, nil
		}
		if secrets == nil {
			return nil, pluginsdk.NewError(pluginsdk.KindAuth, "no secret resolver available", nil)
		}
		val, err := secrets.Resolve(x)
		if err != nil {
			return nil, pluginsdk.NewError(pluginsdk.KindAuth, fmt.Sprintf("resolving %s failed", x), err)
		}
		return val, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			r, err := resolveValue(e, secrets)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			r, err := resolveValue(e, secrets)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return v, nil
}

func writeVault(dir string, in Input, req pluginsdk.ExecRequest) (string, error) {
	if in.VaultPassword == "" {
		return "", nil
	}
	if req.Secrets == nil {
		return "", pluginsdk.NewError(pluginsdk.KindAuth, "no secret resolver available", nil)
	}
	pw, err := req.Secrets.Resolve(in.VaultPassword)
	if err != nil {
		return "", pluginsdk.NewError(pluginsdk.KindAuth, "resolving the vault password "+in.VaultPassword+" failed", err)
	}
	path := filepath.Join(dir, "vault_password")
	return path, writeSecret(path, []byte(pw+"\n"))
}

// reportHostKeys reports the keys ssh recorded for tofu nodes that had none, so
// the controller can store them (ADR-0046). It also runs after a failed or
// cancelled play: a key seen is worth keeping.
func reportHostKeys(req pluginsdk.ExecRequest, pl plan) {
	if req.HostKeys == nil || len(pl.tofu) == 0 {
		return
	}
	raw, err := os.ReadFile(pl.knownHosts)
	if err != nil {
		return
	}
	lines := strings.Split(string(raw), "\n")
	for _, name := range pl.tofu {
		for _, l := range lines {
			if key, ok := strings.CutPrefix(l, name+" "); ok && strings.TrimSpace(key) != "" {
				req.HostKeys.ReportHostKey(name, strings.TrimSpace(key))
				break
			}
		}
	}
}

func marshalResult(rr runOutcome, commit string, code int) (pluginsdk.Result, error) {
	out := Output{Stats: rr.stats, Commit: commit, ExitCode: code, FailedHosts: []string{}, ChangedHosts: []string{}}
	if out.Stats == nil {
		out.Stats = map[string]HostStats{}
	}
	for _, h := range sortedHosts(out.Stats) {
		s := out.Stats[h]
		if s.Failed > 0 || s.Unreachable > 0 {
			out.FailedHosts = append(out.FailedHosts, h)
		}
		if s.Changed > 0 {
			out.ChangedHosts = append(out.ChangedHosts, h)
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return pluginsdk.Result{}, errors.New("encoding ansible output failed")
	}
	return pluginsdk.Result{Outputs: raw}, nil
}
