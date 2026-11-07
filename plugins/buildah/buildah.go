// Package buildah is the built-in buildah plugin: it builds a container image
// from a git repository and pushes it, reporting the registry digest
// (ARCHITECTURE.md section 6.5). It runs on the agent itself and belongs in a
// dedicated build pool, because a Containerfile is arbitrary code. It uses only
// pkg/pluginsdk and plugins/internal/*.
package buildah

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/internal/gitcheckout"
	"github.com/denyszorinets/damson/plugins/internal/proc"
)

// Config keys set by the agent from operator settings.
const (
	// ConfigAllowInsecureRegistry permits tls_verify=false
	// (agent.allow_insecure_registry).
	ConfigAllowInsecureRegistry = "allow_insecure_registry"
	// ConfigAllowInsecureHostKeys permits ssh repositories without known_hosts
	// (agent.allow_insecure_host_keys).
	ConfigAllowInsecureHostKeys = "allow_insecure_host_keys"
	// ConfigAllowInsecureGit permits http, git and file repository URLs
	// (agent.allow_insecure_git).
	ConfigAllowInsecureGit = "allow_insecure_git"
)

// Plugins returns the plugins in this package.
func Plugins() []pluginsdk.Plugin {
	return []pluginsdk.Plugin{plugin{manifest: pluginsdk.Manifest{
		Name:         "buildah",
		Version:      "0.1.0",
		Title:        "Buildah",
		Description:  "Builds a container image from a git repository with buildah and pushes it.",
		InputSchema:  pluginsdk.MustSchemaFor[Input](),
		OutputSchema: pluginsdk.MustSchemaFor[Output](),
		Idempotent:   false,
		Requires:     []string{"bin:buildah", "bin:git"},
		TargetMode:   pluginsdk.TargetNone,
	}}}
}

type plugin struct{ manifest pluginsdk.Manifest }

func (p plugin) Manifest() pluginsdk.Manifest { return p.manifest }

func (plugin) Validate(_ context.Context, input json.RawMessage) error {
	_, err := decode(input)
	return err
}

func (plugin) Execute(ctx context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
	out, err := execute(ctx, req, emit)
	res := pluginsdk.Result{}
	if raw, merr := json.Marshal(out); merr == nil && out.Image != "" {
		res.Outputs = raw
	}
	if err != nil {
		res.Error = pluginsdk.AsStepError(err)
	}
	return res, nil
}

func execute(ctx context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (Output, error) {
	if err := ctx.Err(); err != nil {
		return Output{}, err
	}
	in, err := decode(req.Input)
	if err != nil {
		return Output{}, err
	}
	if !in.tlsVerify() && req.Config[ConfigAllowInsecureRegistry] != "true" {
		return Output{}, pluginsdk.ValidationError("tls_verify=false is not enabled on this agent (%s)", ConfigAllowInsecureRegistry)
	}
	if req.WorkDir == "" {
		return Output{}, pluginsdk.NewError(pluginsdk.KindInternal, "no work directory", nil)
	}
	creds := filepath.Join(req.WorkDir, "creds")
	if err := os.MkdirAll(creds, 0o700); err != nil {
		return Output{}, pluginsdk.NewError(pluginsdk.KindInternal, "creating the credentials directory failed", err)
	}
	defer func() { _ = os.RemoveAll(creds) }()

	// Credentials come first: a missing secret fails the step before a clone.
	authFile, err := writeAuth(creds, in, req)
	if err != nil {
		return Output{}, err
	}
	secretArgs, err := writeBuildSecrets(creds, in, req)
	if err != nil {
		return Output{}, err
	}

	repoDir := filepath.Join(req.WorkDir, "repo")
	co, err := checkout(ctx, repoDir, in.Repo, req, emit)
	if err != nil {
		return Output{}, err
	}
	emit.Log(pluginsdk.System, "checked out "+co.Commit)
	out := Output{Image: in.Image, Tags: tagsOf(in, co.Commit), Commit: co.Commit}

	ctxDir, containerfile, err := buildPaths(repoDir, in)
	if err != nil {
		return out, err
	}
	if err := prepareDirs(req.WorkDir); err != nil {
		return out, err
	}
	b := builder{in: in, env: environment(req.WorkDir, authFile), root: filepath.Join(req.WorkDir, "storage"), runroot: filepath.Join(req.WorkDir, "run")}
	defer b.cleanup(emit)

	if err := b.build(ctx, repoDir, ctxDir, containerfile, out.Tags, authFile, secretArgs, emit); err != nil {
		return out, err
	}
	if !in.push() {
		emit.Log(pluginsdk.System, "push is false: the image stays local and has no registry digest")
		return out, nil
	}
	digest, err := b.push(ctx, req.WorkDir, out.Tags, authFile, emit)
	if err != nil {
		return out, err
	}
	out.Digest, out.ImageRef = digest, in.Image+"@"+digest
	return out, nil
}

// checkout resolves the repository credential and clones it.
func checkout(ctx context.Context, dir string, r Repo, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (gitcheckout.Result, error) {
	var auth gitcheckout.Auth
	if r.Credential != "" {
		if req.Secrets == nil {
			return gitcheckout.Result{}, pluginsdk.NewError(pluginsdk.KindAuth, "no secret resolver available", nil)
		}
		f, err := req.Secrets.ResolveFields(r.Credential)
		if err != nil {
			return gitcheckout.Result{}, pluginsdk.NewError(pluginsdk.KindAuth, "resolving credential "+r.Credential+" failed", err)
		}
		tok := f["token"]
		if tok == "" {
			tok = f["password"]
		}
		auth = gitcheckout.Auth{Username: f["username"], Token: tok, SSHKey: f["private_key"], KnownHosts: f["known_hosts"]}
	}
	auth.InsecureHostKey = req.Config[ConfigAllowInsecureHostKeys] == "true"
	repo := gitcheckout.Repo{URL: r.URL, Ref: r.Ref, Submodules: r.Submodules, AllowInsecure: req.Config[ConfigAllowInsecureGit] == "true"}
	return gitcheckout.Clone(ctx, dir, repo, auth, gitcheckout.Options{Out: &proc.LineWriter{Emit: emit, Stream: pluginsdk.System}})
}
