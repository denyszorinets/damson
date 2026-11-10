package buildah

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// resolveInRepo resolves p under repoDir through symlinks and requires the
// result to stay inside the checkout. The absolute result is what buildah gets:
// a relative value such as "https:/x" or "github.com/x/y" could be read as a
// remote context and fetched with the agent user's own git credentials, and a
// symlink could point the context at the credential files or at the host.
func resolveInRepo(repoDir, what, p string) (string, error) {
	root, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		return "", pluginsdk.NewError(pluginsdk.KindInternal, "resolving the checkout failed", err)
	}
	full, err := filepath.EvalSymlinks(filepath.Join(root, p))
	if err != nil {
		return "", pluginsdk.ValidationError("%s %q is not in the repository", what, p)
	}
	if rel, rerr := filepath.Rel(root, full); rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", pluginsdk.ValidationError("%s %q resolves outside the repository", what, p)
	}
	return full, nil
}

// buildPaths returns the absolute build context and Containerfile.
func buildPaths(repoDir string, in Input) (ctxDir, containerfile string, err error) {
	rel := in.Context
	if rel == "" {
		rel = "."
	}
	if ctxDir, err = resolveInRepo(repoDir, "context", rel); err != nil {
		return "", "", err
	}
	if st, serr := os.Stat(ctxDir); serr != nil || !st.IsDir() {
		return "", "", pluginsdk.ValidationError("context %q is not a directory in the repository", rel)
	}
	candidates := []string{in.Containerfile}
	if in.Containerfile == "" {
		candidates = []string{filepath.Join(rel, "Containerfile"), filepath.Join(rel, "Dockerfile")}
	}
	for _, c := range candidates {
		full, rerr := resolveInRepo(repoDir, "containerfile", c)
		if rerr != nil {
			var se *pluginsdk.StepError
			if errors.As(rerr, &se) && strings.Contains(se.Message, "resolves outside") {
				return "", "", rerr
			}
			continue
		}
		if st, serr := os.Stat(full); serr == nil && st.Mode().IsRegular() {
			return ctxDir, full, nil
		}
	}
	if in.Containerfile != "" {
		return "", "", pluginsdk.ValidationError("containerfile %q is not in the repository", in.Containerfile)
	}
	return "", "", pluginsdk.ValidationError("the build context has no Containerfile or Dockerfile")
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
// agent's own except what buildah needs to find its tools, its configuration
// and the network. HOME, the runtime directory and the temporary directory are
// private to the step, so the agent user's registry logins (~/.docker, XDG
// auth.json) and caches are never read and no layer temp file outlives the work
// directory.
func environment(workDir, authFile string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"), "LC_ALL=C.UTF-8",
		"HOME=" + filepath.Join(workDir, "home"),
		"XDG_RUNTIME_DIR=" + filepath.Join(workDir, "xdg"),
		"TMPDIR=" + filepath.Join(workDir, "tmp"),
		"BUILDAH_TMPDIR=" + filepath.Join(workDir, "tmp"),
	}
	if authFile != "" {
		env = append(env, "REGISTRY_AUTH_FILE="+authFile)
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "BUILDAH_TMPDIR":
		case strings.HasPrefix(k, "BUILDAH_"), strings.HasPrefix(k, "CONTAINERS_"), strings.HasPrefix(k, "_CONTAINERS_"), strings.HasPrefix(k, "STORAGE_"),
			k == "SSL_CERT_FILE", k == "SSL_CERT_DIR", k == "HTTPS_PROXY", k == "https_proxy", k == "HTTP_PROXY", k == "http_proxy", k == "NO_PROXY", k == "no_proxy":
			env = append(env, kv)
		}
	}
	return env
}

// prepareDirs creates the private directories of environment.
func prepareDirs(workDir string) error {
	for _, d := range []string{"home", "xdg", "tmp"} {
		if err := os.MkdirAll(filepath.Join(workDir, d), 0o700); err != nil {
			return pluginsdk.NewError(pluginsdk.KindInternal, "preparing the work directory failed", err)
		}
	}
	return nil
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

func (b builder) buildArgs(ctxDir, containerfile string, tags []string, authFile string, secretArgs []string) []string {
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
		args = append(args, "--target="+b.in.Target)
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
	return append(args, "--", ctxDir)
}

func (b builder) build(ctx context.Context, repoDir, ctxDir, containerfile string, tags []string, authFile string, secretArgs []string, emit pluginsdk.Emitter) error {
	emit.Log(pluginsdk.System, "building "+b.in.Image+" with tags "+strings.Join(tags, ", "))
	res, err := proc.Run(ctx, proc.Spec{Dir: repoDir, Env: b.env, Name: "buildah", Args: b.buildArgs(ctxDir, containerfile, tags, authFile, secretArgs)}, emit)
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

// cleanup removes the working containers, the local images and the storage,
// best effort and even after a cancel. Rootless overlay storage is owned by
// subuids, so the storage is removed from inside the user namespace; the agent
// removes the rest of the work directory afterwards.
func (b builder) cleanup(emit pluginsdk.Emitter) {
	if _, err := os.Stat(b.root); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	steps := [][]string{
		append(b.global(), "rm", "--all"),
		append(b.global(), "rmi", "--all", "--force"),
		append(b.global(), "unshare", "rm", "-rf", b.root, b.runroot),
	}
	for _, args := range steps {
		res, err := proc.Run(ctx, proc.Spec{Env: b.env, Name: "buildah", Args: args}, discard{})
		if err != nil || res.ExitCode != 0 {
			emit.Log(pluginsdk.System, "local cleanup step \""+args[len(b.global())]+"\" did not finish; the work directory is removed anyway")
		}
	}
}

// discard drops cleanup output.
type discard struct{}

func (discard) Log(pluginsdk.Stream, string) {}
func (discard) Progress(float64, string)     {}

// classify maps a failed buildah command to a step error. Only buildah's own
// "Error:" lines are read: the stderr tail also holds the output of the
// Containerfile's RUN steps, which can say "connection refused" or "denied" for
// reasons that have nothing to do with the registry.
func classify(phase string, res proc.Result) error {
	low := strings.ToLower(errorLines(res.Stderr))
	msg := fmt.Sprintf("buildah %s exited with code %d", phase, res.ExitCode)
	switch {
	case containsAny(low, "unauthorized", "authentication required", "denied:", "requested access to the resource is denied", "invalid username/password", "access forbidden"):
		return pluginsdk.NewError(pluginsdk.KindAuth, msg+": registry authentication failed", nil)
	case containsAny(low, "no such host", "connection refused", "i/o timeout", "network is unreachable", "tls handshake timeout", "x509:", "certificate", "no route to host", "dial tcp", "connection reset"):
		return pluginsdk.NewError(pluginsdk.KindConnection, msg+": the registry or network is unreachable", nil)
	}
	return pluginsdk.ExitCodeError(res.ExitCode, msg)
}

// errorLines returns the lines of s that buildah itself reports as errors.
func errorLines(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "Error:") || strings.HasPrefix(t, "error:") {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n")
}

func containsAny(s string, subs ...string) bool {
	for _, p := range subs {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
