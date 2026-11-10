// Package helm is the built-in helm plugin: it installs, upgrades, rolls back,
// uninstalls and inspects Helm releases (ARCHITECTURE.md section 6.6). It runs on
// the agent itself and uses only pkg/pluginsdk and plugins/internal/*.
package helm

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
	// ConfigAllowInCluster permits cluster.in_cluster, which deploys with the
	// agent's own service account (agent.allow_helm_in_cluster).
	ConfigAllowInCluster = "allow_in_cluster"
	// ConfigAllowInsecureHostKeys and ConfigAllowInsecureGit are used by the git
	// chart source (agent.allow_insecure_host_keys, agent.allow_insecure_git).
	ConfigAllowInsecureHostKeys = gitcheckout.ConfigAllowInsecureHostKeys
	ConfigAllowInsecureGit      = gitcheckout.ConfigAllowInsecureGit
)

// Plugins returns the plugins in this package.
func Plugins() []pluginsdk.Plugin {
	return []pluginsdk.Plugin{plugin{manifest: pluginsdk.Manifest{
		Name:         "helm",
		Version:      "0.1.0",
		Title:        "Helm",
		Description:  "Installs, upgrades, rolls back, uninstalls or inspects a Helm release.",
		InputSchema:  pluginsdk.MustSchemaFor[Input](),
		OutputSchema: pluginsdk.MustSchemaFor[Output](),
		Idempotent:   false,
		Requires:     []string{"bin:helm", "bin:git"},
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
	if out.Release != "" {
		if raw, merr := json.Marshal(out); merr == nil {
			res.Outputs = raw
		}
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
	if in.Cluster.InCluster && req.Config[ConfigAllowInCluster] != "true" {
		return Output{}, pluginsdk.ValidationError("cluster.in_cluster is not enabled on this agent (%s)", ConfigAllowInCluster)
	}
	if req.WorkDir == "" {
		return Output{}, pluginsdk.NewError(pluginsdk.KindInternal, "no work directory", nil)
	}
	creds := filepath.Join(req.WorkDir, "creds")
	if err := os.MkdirAll(creds, 0o700); err != nil {
		return Output{}, pluginsdk.NewError(pluginsdk.KindInternal, "creating the credentials directory failed", err)
	}
	defer func() { _ = os.RemoveAll(creds) }()
	if err := prepareDirs(req.WorkDir); err != nil {
		return Output{}, err
	}

	// Everything that needs a secret is resolved before the clone and before helm.
	files, err := writeFiles(creds, in, req)
	if err != nil {
		return Output{}, err
	}
	r := runner{in: in, files: files, env: environment(req.WorkDir, in, files), workDir: req.WorkDir}

	if in.Action == actionUpgrade {
		if err := r.resolveChart(ctx, req, emit); err != nil {
			return Output{}, err
		}
	}
	return r.run(ctx, emit)
}

// checkout clones the git chart source.
func checkout(ctx context.Context, dir string, g *GitChart, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) error {
	res, err := gitcheckout.CloneFor(ctx, dir, gitcheckout.Spec{URL: g.URL, Ref: g.Ref, Credential: g.Credential}, req,
		&proc.LineWriter{Emit: emit, Stream: pluginsdk.System})
	if err == nil {
		emit.Log(pluginsdk.System, "checked out "+res.Commit)
	}
	return err
}
