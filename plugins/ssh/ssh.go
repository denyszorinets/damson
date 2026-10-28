// Package ssh is the built-in ssh plugin: it runs a command or script on one
// inventory node over SSH (ARCHITECTURE.md section 6.1). It uses only
// pkg/pluginsdk and plugins/internal/*.
package ssh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	gossh "golang.org/x/crypto/ssh"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Plugins returns the plugins in this package.
func Plugins() []pluginsdk.Plugin {
	return []pluginsdk.Plugin{plugin{manifest: pluginsdk.Manifest{
		Name:         "ssh",
		Version:      "0.1.0",
		Title:        "SSH",
		Description:  "Runs a command or script on a node over SSH.",
		InputSchema:  pluginsdk.MustSchemaFor[Input](),
		OutputSchema: pluginsdk.MustSchemaFor[Output](),
		Idempotent:   false,
		TargetMode:   pluginsdk.TargetSingleNode,
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
	node, ok := req.Nodes[in.Node]
	if !ok {
		return pluginsdk.Result{}, pluginsdk.ValidationError("node %q is not part of this step", in.Node)
	}
	env, err := resolveEnv(in.Env, req.Secrets)
	if err != nil {
		return pluginsdk.Result{}, err
	}
	call, err := buildCall(in, env)
	if err != nil {
		return pluginsdk.Result{}, err
	}

	client, hk, err := connect(ctx, req, node, in)
	if err != nil {
		return pluginsdk.Result{}, err
	}
	defer func() { _ = client.Close() }() // may already be closed by run
	logger(req).Debug("ssh connected", "node", node.Name, "host_key_policy", hk.policy)
	reportHostKey(req, node, hk, emit)

	rr, runErr := run(ctx, client, in, call, emit)
	res := pluginsdk.Result{}
	if out, err := marshalOutput(rr); err == nil {
		res.Outputs = out
	} else {
		return res, err
	}
	switch {
	case runErr != nil:
		return res, runErr
	case !in.successful(rr.ExitCode):
		return res, pluginsdk.ExitCodeError(rr.ExitCode, fmt.Sprintf("command exited with code %d", rr.ExitCode))
	}
	return res, nil
}

// reportHostKey hands a trust-on-first-use key to the host. A pinned key that
// matched is not reported.
func reportHostKey(req pluginsdk.ExecRequest, node pluginsdk.NodeInfo, hk *hostKeyCheck, emit pluginsdk.Emitter) {
	switch {
	case hk.policy == policyInsecure:
		emit.Log(pluginsdk.System, "warning: host key of "+node.Name+" was not verified (insecure host key policy)")
	case hk.policy == policyTofu && hk.pinned == nil && hk.observed != nil && req.HostKeys != nil:
		line := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(hk.observed)))
		req.HostKeys.ReportHostKey(node.Name, line)
		emit.Log(pluginsdk.System, "trusting host key "+gossh.FingerprintSHA256(hk.observed)+" of "+node.Name+" on first use")
	}
}

// resolveEnv returns env with secret:// values resolved from the bundle.
func resolveEnv(env map[string]string, secrets pluginsdk.SecretResolver) (map[string]string, error) {
	out := make(map[string]string, len(env))
	for k, v := range env {
		if strings.HasPrefix(v, "secret://") {
			if secrets == nil {
				return nil, pluginsdk.NewError(pluginsdk.KindAuth, "no secret resolver available for env "+k, nil)
			}
			rv, err := secrets.Resolve(v)
			if err != nil {
				return nil, pluginsdk.NewError(pluginsdk.KindAuth, fmt.Sprintf("resolving env %s (%s) failed", k, v), err)
			}
			v = rv
		}
		out[k] = v
	}
	return out, nil
}

func marshalOutput(rr runResult) (json.RawMessage, error) {
	raw, err := json.Marshal(Output{ExitCode: rr.ExitCode, StdoutTail: rr.Stdout, StderrTail: rr.Stderr, Values: rr.Values})
	if err != nil {
		return nil, errors.New("encoding ssh output failed")
	}
	return raw, nil
}

func logger(req pluginsdk.ExecRequest) *slog.Logger {
	if req.Logger != nil {
		return req.Logger
	}
	return slog.New(slog.DiscardHandler)
}
