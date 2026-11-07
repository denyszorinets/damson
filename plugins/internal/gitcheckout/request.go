package gitcheckout

import (
	"context"
	"io"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Config keys the agent sets for the plugins that clone (from
// agent.allow_insecure_host_keys and agent.allow_insecure_git).
const (
	ConfigAllowInsecureHostKeys = "allow_insecure_host_keys"
	ConfigAllowInsecureGit      = "allow_insecure_git"
)

// Spec is the repository a step names: the URL, the ref, the optional
// credential ref and whether to clone submodules.
type Spec struct {
	URL        string
	Ref        string
	Credential string // secret:// ref: a token or password (https) or an ssh_key (ssh)
	Submodules bool
}

// CloneFor clones spec into dir for a step: it resolves the credential from the
// request's secrets and applies the operator settings in req.Config. A missing
// credential is an auth error. Output of git goes to out.
func CloneFor(ctx context.Context, dir string, spec Spec, req pluginsdk.ExecRequest, out io.Writer) (Result, error) {
	var auth Auth
	if spec.Credential != "" {
		if req.Secrets == nil {
			return Result{}, pluginsdk.NewError(pluginsdk.KindAuth, "no secret resolver available", nil)
		}
		f, err := req.Secrets.ResolveFields(spec.Credential)
		if err != nil {
			return Result{}, pluginsdk.NewError(pluginsdk.KindAuth, "resolving credential "+spec.Credential+" failed", err)
		}
		tok := f["token"]
		if tok == "" {
			tok = f["password"]
		}
		auth = Auth{Username: f["username"], Token: tok, SSHKey: f["private_key"], KnownHosts: f["known_hosts"]}
	}
	auth.InsecureHostKey = req.Config[ConfigAllowInsecureHostKeys] == "true"
	repo := Repo{URL: spec.URL, Ref: spec.Ref, Submodules: spec.Submodules, AllowInsecure: req.Config[ConfigAllowInsecureGit] == "true"}
	return Clone(ctx, dir, repo, auth, Options{Out: out})
}
