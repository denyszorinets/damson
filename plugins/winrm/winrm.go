// Package winrm is the built-in winrm plugin: it runs a command or a PowerShell
// script on one inventory node over WinRM (ARCHITECTURE.md section 6.2). It
// uses only pkg/pluginsdk and plugins/internal/*.
package winrm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Plugins returns the plugins in this package.
func Plugins() []pluginsdk.Plugin {
	return []pluginsdk.Plugin{plugin{manifest: pluginsdk.Manifest{
		Name:         "winrm",
		Version:      "0.1.0",
		Title:        "WinRM",
		Description:  "Runs a command or a PowerShell script on a Windows node over WinRM.",
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
	if err := checkSecurity(req, in); err != nil {
		return pluginsdk.Result{}, err
	}
	node, ok := req.Nodes[in.Node]
	if !ok {
		return pluginsdk.Result{}, pluginsdk.ValidationError("node %q is not part of this step", in.Node)
	}
	if node.Transport != "" && node.Transport != "winrm" {
		return pluginsdk.Result{}, pluginsdk.ValidationError("node %q uses transport %q; the winrm plugin needs a winrm node", node.Name, node.Transport)
	}
	env, err := resolveEnv(in.Env, req.Secrets)
	if err != nil {
		return pluginsdk.Result{}, err
	}
	payload, err := stdinPayload(in, env)
	if err != nil {
		return pluginsdk.Result{}, err
	}
	user, pass, err := credentials(req, node, in.Credential)
	if err != nil {
		return pluginsdk.Result{}, err
	}
	client, release, err := newClient(req, node, in, user, pass)
	if err != nil {
		return pluginsdk.Result{}, err
	}
	defer release()
	if in.Transport == transportHTTP {
		emit.Log(pluginsdk.System, "warning: winrm to "+node.Name+" uses plain http (ntlm message encryption); the server is not authenticated")
	}
	if in.InsecureSkipVerify {
		emit.Log(pluginsdk.System, "warning: the certificate of "+node.Name+" is not verified (insecure_skip_verify)")
	}

	rr, runErr := run(ctx, client, payload, emit)
	res := pluginsdk.Result{}
	raw, merr := json.Marshal(Output{ExitCode: rr.ExitCode, StdoutTail: rr.Stdout, StderrTail: rr.Stderr, Values: rr.Values})
	if merr != nil {
		return res, errors.New("encoding winrm output failed")
	}
	res.Outputs = raw
	switch {
	case ctx.Err() != nil:
		return res, ctx.Err()
	case runErr != nil:
		return res, classify(runErr, node.Address)
	case !in.successful(rr.ExitCode):
		return res, pluginsdk.ExitCodeError(rr.ExitCode, fmt.Sprintf("command exited with code %d", rr.ExitCode))
	}
	return res, nil
}

// resolveEnv returns env with secret:// values resolved from the bundle.
func resolveEnv(env map[string]string, secrets pluginsdk.SecretResolver) (map[string]string, error) {
	if len(env) == 0 {
		return nil, nil
	}
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
