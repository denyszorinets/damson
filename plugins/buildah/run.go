package buildah

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/internal/proc"
)

var digestRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// tagsOf returns the tags to apply: the input's, else the short commit SHA.
func tagsOf(in Input, commit string) []string {
	if len(in.Tags) > 0 {
		return uniq(in.Tags)
	}
	if len(commit) > 12 {
		commit = commit[:12]
	}
	return []string{commit}
}

func uniq(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// findContainerfile returns the Containerfile path relative to the checkout.
func findContainerfile(repoDir string, in Input) (string, error) {
	candidates := []string{in.Containerfile}
	if in.Containerfile == "" {
		base := in.Context
		if base == "" {
			base = "."
		}
		candidates = []string{filepath.Join(base, "Containerfile"), filepath.Join(base, "Dockerfile")}
	}
	for _, c := range candidates {
		if st, err := os.Stat(filepath.Join(repoDir, c)); err == nil && st.Mode().IsRegular() {
			return c, nil
		}
	}
	if in.Containerfile != "" {
		return "", pluginsdk.ValidationError("containerfile %q is not in the repository", in.Containerfile)
	}
	return "", pluginsdk.ValidationError("the build context has no Containerfile or Dockerfile")
}

// writeAuth resolves the registry credentials and writes the registry auth
// file (0600). It returns "" when there are no credentials.
func writeAuth(dir string, in Input, req pluginsdk.ExecRequest) (string, error) {
	if len(in.RegistryCredentials) == 0 {
		return "", nil
	}
	if req.Secrets == nil {
		return "", pluginsdk.NewError(pluginsdk.KindAuth, "no secret resolver available", nil)
	}
	auths := map[string]map[string]string{}
	for _, rc := range in.RegistryCredentials {
		f, err := req.Secrets.ResolveFields(rc.Credential)
		if err != nil {
			return "", pluginsdk.NewError(pluginsdk.KindAuth, "resolving credential "+rc.Credential+" failed", err)
		}
		if f["username"] == "" || f["password"] == "" {
			return "", pluginsdk.NewError(pluginsdk.KindAuth, "credential "+rc.Credential+" needs a username and a password", nil)
		}
		auths[rc.Registry] = map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(f["username"] + ":" + f["password"]))}
	}
	raw, err := json.Marshal(map[string]any{"auths": auths})
	if err != nil {
		return "", pluginsdk.NewError(pluginsdk.KindInternal, "encoding the registry auth failed", err)
	}
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", pluginsdk.NewError(pluginsdk.KindInternal, "writing the registry auth failed", err)
	}
	return path, nil
}

// writeBuildSecrets writes each build secret to a 0600 file and returns the
// --secret arguments. The values are never on the command line.
func writeBuildSecrets(dir string, in Input, req pluginsdk.ExecRequest) ([]string, error) {
	var args []string
	for _, id := range sortedKeys(in.BuildSecrets) {
		ref := in.BuildSecrets[id]
		if req.Secrets == nil {
			return nil, pluginsdk.NewError(pluginsdk.KindAuth, "no secret resolver available", nil)
		}
		v, err := req.Secrets.Resolve(ref)
		if err != nil {
			return nil, pluginsdk.NewError(pluginsdk.KindAuth, "resolving build secret "+id+" ("+ref+") failed", err)
		}
		path := filepath.Join(dir, "secret-"+id)
		if err := os.WriteFile(path, []byte(v), 0o600); err != nil {
			return nil, pluginsdk.NewError(pluginsdk.KindInternal, "writing a build secret failed", err)
		}
		args = append(args, "--secret", "id="+id+",src="+path)
	}
	return args, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// environment is the minimal environment of buildah commands: nothing of the
// agent's own except what buildah needs to find its tools, its configuration and
// the network.
func environment(workDir, authFile string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C.UTF-8"}
	if h := os.Getenv("HOME"); h != "" {
		env = append(env, "HOME="+h)
	}
	if authFile != "" {
		env = append(env, "REGISTRY_AUTH_FILE="+authFile)
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "BUILDAH_"), strings.HasPrefix(k, "CONTAINERS_"), strings.HasPrefix(k, "_CONTAINERS_"), strings.HasPrefix(k, "STORAGE_"),
			k == "XDG_RUNTIME_DIR", k == "XDG_CONFIG_HOME", k == "XDG_DATA_HOME", k == "TMPDIR",
			k == "SSL_CERT_FILE", k == "SSL_CERT_DIR", k == "HTTPS_PROXY", k == "https_proxy", k == "HTTP_PROXY", k == "http_proxy", k == "NO_PROXY", k == "no_proxy":
			env = append(env, kv)
		}
	}
	return env
}

type builder struct {
	in            Input
	env           []string
	root, runroot string
}

// global returns the buildah options that keep all storage in the work
// directory, so the agent stays stateless.
func (b builder) global() []string {
	return []string{"--root", b.root, "--runroot", b.runroot}
}

func (b builder) tlsArg() string { return fmt.Sprintf("--tls-verify=%t", b.in.tlsVerify()) }

func (b builder) buildArgs(containerfile string, tags []string, authFile string, secretArgs []string) []string {
	args := append(b.global(), "build", "--layers", b.tlsArg(), "-f", containerfile)
	if authFile != "" {
		args = append(args, "--authfile", authFile)
	}
	for _, t := range tags {
		args = append(args, "-t", b.in.Image+":"+t)
	}
	for _, k := range sortedKeys(b.in.BuildArgs) {
		args = append(args, "--build-arg", k+"="+b.in.BuildArgs[k])
	}
	for _, k := range sortedKeys(b.in.Labels) {
		args = append(args, "--label", k+"="+b.in.Labels[k])
	}
	if b.in.Target != "" {
		args = append(args, "--target", b.in.Target)
	}
	if b.in.Platform != "" {
		args = append(args, "--platform", b.in.Platform)
	}
	if c := b.in.Cache; c != nil {
		if c.From != "" {
			args = append(args, "--cache-from", c.From)
		}
		if c.To != "" {
			args = append(args, "--cache-to", c.To)
		}
	}
	args = append(args, secretArgs...)
	ctxDir := b.in.Context
	if ctxDir == "" {
		ctxDir = "."
	}
	return append(args, "--", ctxDir)
}

func (b builder) build(ctx context.Context, repoDir, containerfile string, tags []string, authFile string, secretArgs []string, emit pluginsdk.Emitter) error {
	emit.Log(pluginsdk.System, "building "+b.in.Image+" with tags "+strings.Join(tags, ", "))
	res, err := proc.Run(ctx, proc.Spec{Dir: repoDir, Env: b.env, Name: "buildah", Args: b.buildArgs(containerfile, tags, authFile, secretArgs)}, emit)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return classify("build", res)
	}
	return nil
}

// push pushes every tag and returns the digest. All tags name the same image, so
// the digests must agree.
func (b builder) push(ctx context.Context, workDir string, tags []string, authFile string, emit pluginsdk.Emitter) (string, error) {
	digest := ""
	for _, t := range tags {
		ref := b.in.Image + ":" + t
		file := filepath.Join(workDir, "digest-"+t)
		args := append(b.global(), "push", b.tlsArg(), "--digestfile", file)
		if authFile != "" {
			args = append(args, "--authfile", authFile)
		}
		args = append(args, ref, "docker://"+ref)
		emit.Log(pluginsdk.System, "pushing "+ref)
		res, err := proc.Run(ctx, proc.Spec{Dir: workDir, Env: b.env, Name: "buildah", Args: args}, emit)
		if err != nil {
			return "", err
		}
		if res.ExitCode != 0 {
			return "", classify("push", res)
		}
		raw, err := os.ReadFile(file)
		d := strings.TrimSpace(string(raw))
		if err != nil || !digestRe.MatchString(d) {
			return "", pluginsdk.NewError(pluginsdk.KindInternal, "buildah push wrote no valid digest for "+ref, err)
		}
		if digest != "" && digest != d {
			return "", pluginsdk.NewError(pluginsdk.KindInternal, "the tags were pushed with different digests", nil)
		}
		digest = d
	}
	return digest, nil
}

// cleanup removes the local images, best effort and even after a cancel. The
// storage is inside the work directory, which the agent removes afterwards.
func (b builder) cleanup(tags []string, emit pluginsdk.Emitter) {
	if _, err := os.Stat(b.root); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	args := append(b.global(), "rmi", "--all", "--force")
	if _, err := proc.Run(ctx, proc.Spec{Env: b.env, Name: "buildah", Args: args}, discard{}); err != nil {
		emit.Log(pluginsdk.System, "could not remove the local images; the work directory is removed anyway")
	}
}

// discard drops cleanup output.
type discard struct{}

func (discard) Log(pluginsdk.Stream, string) {}
func (discard) Progress(float64, string)     {}

// classify maps a failed buildah command to a step error from its stderr.
func classify(phase string, res proc.Result) error {
	low := strings.ToLower(res.Stderr)
	msg := fmt.Sprintf("buildah %s exited with code %d", phase, res.ExitCode)
	switch {
	case containsAny(low, "unauthorized", "authentication required", "denied:", "requested access to the resource is denied", "invalid username/password", "access forbidden"):
		return pluginsdk.NewError(pluginsdk.KindAuth, msg+": registry authentication failed", nil)
	case containsAny(low, "no such host", "connection refused", "i/o timeout", "network is unreachable", "tls handshake timeout", "x509:", "certificate", "no route to host", "dial tcp", "connection reset"):
		return pluginsdk.NewError(pluginsdk.KindConnection, msg+": the registry or network is unreachable", nil)
	}
	return pluginsdk.ExitCodeError(res.ExitCode, msg)
}

func containsAny(s string, subs ...string) bool {
	for _, p := range subs {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
