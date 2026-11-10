package helm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/internal/proc"
)

// files are the credential files written for a step (empty when not needed).
type files struct {
	kubeconfig string
	values     string
	registry   string
}

// writeFiles resolves the kubeconfig, the values and the registry logins and
// writes them to 0600 files. Nothing secret is on a command line.
func writeFiles(dir string, in Input, req pluginsdk.ExecRequest) (files, error) {
	var f files
	if in.Cluster.Kubeconfig != "" {
		kc, err := kubeconfig(in.Cluster.Kubeconfig, req)
		if err != nil {
			return f, err
		}
		f.kubeconfig = filepath.Join(dir, "kubeconfig")
		if err := writeSecret(f.kubeconfig, []byte(kc)); err != nil {
			return f, err
		}
	}
	if in.Action != actionUpgrade {
		return f, nil
	}
	if len(in.Values) > 0 {
		resolved, err := resolveValue(in.Values, req.Secrets)
		if err != nil {
			return f, err
		}
		raw, err := json.Marshal(resolved)
		if err != nil {
			return f, pluginsdk.ValidationError("values cannot be encoded")
		}
		f.values = filepath.Join(dir, "values.json")
		if err := writeSecret(f.values, raw); err != nil {
			return f, err
		}
	}
	if len(in.RegistryCredentials) > 0 {
		auths := map[string]map[string]string{}
		for _, rc := range in.RegistryCredentials {
			if req.Secrets == nil {
				return f, pluginsdk.NewError(pluginsdk.KindAuth, "no secret resolver available", nil)
			}
			c, err := req.Secrets.ResolveFields(rc.Credential)
			if err != nil {
				return f, pluginsdk.NewError(pluginsdk.KindAuth, "resolving credential "+rc.Credential+" failed", err)
			}
			if c["username"] == "" || c["password"] == "" {
				return f, pluginsdk.NewError(pluginsdk.KindAuth, "credential "+rc.Credential+" needs a username and a password", nil)
			}
			auths[rc.Registry] = map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(c["username"] + ":" + c["password"]))}
		}
		raw, _ := json.Marshal(map[string]any{"auths": auths})
		f.registry = filepath.Join(dir, "registry.json")
		if err := writeSecret(f.registry, raw); err != nil {
			return f, err
		}
	}
	return f, nil
}

// kubeconfig resolves cluster.kubeconfig: a ref with a field (secret://t/k#kubeconfig),
// or a whole secret that has a field named kubeconfig.
func kubeconfig(ref string, req pluginsdk.ExecRequest) (string, error) {
	if req.Secrets == nil {
		return "", pluginsdk.NewError(pluginsdk.KindAuth, "no secret resolver available", nil)
	}
	if strings.Contains(ref, "#") {
		v, err := req.Secrets.Resolve(ref)
		if err != nil || v == "" {
			return "", pluginsdk.NewError(pluginsdk.KindAuth, "resolving the kubeconfig "+ref+" failed", err)
		}
		return v, validateKubeconfig(v)
	}
	f, err := req.Secrets.ResolveFields(ref)
	if err != nil || f["kubeconfig"] == "" {
		return "", pluginsdk.NewError(pluginsdk.KindAuth, "the kubeconfig secret "+ref+" cannot be read or has no field kubeconfig", err)
	}
	return f["kubeconfig"], validateKubeconfig(f["kubeconfig"])
}

// validateKubeconfig refuses a kubeconfig that would let the job reach the
// agent's own files or run programs on the agent: client-go reads the file
// fields (tokenFile, certificate-authority, client-certificate, client-key) from
// the agent's disk, so a job could point them at the agent's service account
// token, and exec or auth-provider run a program. Only the inline forms (token,
// certificate-authority-data, client-certificate-data, client-key-data) are
// accepted.
func validateKubeconfig(kc string) error {
	var doc any
	if err := yaml.Unmarshal([]byte(kc), &doc); err != nil {
		return pluginsdk.NewError(pluginsdk.KindValidation, "the kubeconfig is not valid YAML", nil)
	}
	if _, ok := doc.(map[string]any); !ok {
		return pluginsdk.ValidationError("the kubeconfig is not a YAML mapping")
	}
	if bad := forbiddenKubeconfigKey(doc); bad != "" {
		return pluginsdk.ValidationError("the kubeconfig uses %q, which is not allowed: file references and exec or auth-provider plugins are refused; use the inline -data forms and a token", bad)
	}
	return nil
}

func forbiddenKubeconfigKey(v any) string {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			switch k {
			case "exec", "auth-provider", "tokenFile", "certificate-authority", "client-certificate", "client-key":
				return k
			}
			if bad := forbiddenKubeconfigKey(e); bad != "" {
				return bad
			}
		}
	case []any:
		for _, e := range x {
			if bad := forbiddenKubeconfigKey(e); bad != "" {
				return bad
			}
		}
	}
	return ""
}

// checkNoEscapingSymlinks walks the chart directory and refuses a symlink that
// resolves outside the checkout: helm follows symlinks inside a chart, so
// .Files.Get could otherwise read any file the agent can (its service account
// token, other steps' work directories) and render it into a deployment or an
// error message.
func checkNoEscapingSymlinks(chartDir, root string) error {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return pluginsdk.NewError(pluginsdk.KindInternal, "resolving the checkout failed", err)
	}
	return filepath.WalkDir(chartDir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return pluginsdk.NewError(pluginsdk.KindInternal, "reading the chart failed", werr)
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		target, rerr := filepath.EvalSymlinks(p)
		if rerr != nil {
			return pluginsdk.ValidationError("the chart has a broken symlink %q", relTo(chartDir, p))
		}
		if rel, e := filepath.Rel(rootReal, target); e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return pluginsdk.ValidationError("the chart has a symlink %q that resolves outside the repository", relTo(chartDir, p))
		}
		return nil
	})
}

func relTo(base, p string) string {
	if rel, err := filepath.Rel(base, p); err == nil {
		return rel
	}
	return p
}

func writeSecret(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return pluginsdk.NewError(pluginsdk.KindInternal, "writing a credential file failed", err)
	}
	return nil
}

// resolveValue resolves secret:// strings inside values.
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

// prepareDirs creates the private directories of environment.
func prepareDirs(workDir string) error {
	for _, d := range []string{"home", "helm/cache", "helm/config", "helm/data", "tmp"} {
		if err := os.MkdirAll(filepath.Join(workDir, d), 0o700); err != nil {
			return pluginsdk.NewError(pluginsdk.KindInternal, "preparing the work directory failed", err)
		}
	}
	return nil
}

// environment is the minimal environment of helm commands. Helm's cache,
// config and data live in the work directory, KUBECONFIG and the agent's own
// variables are not passed, and for in_cluster only the variables that tell
// client-go where the API server is.
func environment(workDir string, in Input, f files) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"), "LC_ALL=C.UTF-8",
		"HOME=" + filepath.Join(workDir, "home"),
		"TMPDIR=" + filepath.Join(workDir, "tmp"),
		"HELM_CACHE_HOME=" + filepath.Join(workDir, "helm", "cache"),
		"HELM_CONFIG_HOME=" + filepath.Join(workDir, "helm", "config"),
		"HELM_DATA_HOME=" + filepath.Join(workDir, "helm", "data"),
		"HELM_PLUGINS=" + filepath.Join(workDir, "helm", "data", "plugins"),
	}
	if f.registry != "" {
		env = append(env, "HELM_REGISTRY_CONFIG="+f.registry)
	} else {
		env = append(env, "HELM_REGISTRY_CONFIG="+filepath.Join(workDir, "helm", "config", "registry.json"))
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy":
			env = append(env, kv)
		case "KUBERNETES_SERVICE_HOST", "KUBERNETES_SERVICE_PORT":
			if in.Cluster.InCluster {
				env = append(env, kv)
			}
		}
	}
	return env
}

type runner struct {
	in       Input
	files    files
	env      []string
	workDir  string
	chartRef string   // what helm gets as the chart
	extra    []string // chart source flags (--version, --repo, --registry-config)
	valFiles []string
}

// resolveChart prepares the chart source: an OCI or repo reference, or a clone.
func (r *runner) resolveChart(ctx context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) error {
	c := r.in.Chart
	switch {
	case c.OCI != "":
		r.chartRef = c.OCI
		if c.Version != "" {
			r.extra = append(r.extra, "--version", c.Version)
		}
	case c.Repo != nil:
		r.chartRef = c.Repo.Name
		r.extra = append(r.extra, "--repo", c.Repo.URL)
		if c.Repo.Version != "" {
			r.extra = append(r.extra, "--version", c.Repo.Version)
		}
	case c.Git != nil:
		dir := filepath.Join(r.workDir, "chart")
		if err := checkout(ctx, dir, c.Git, req, emit); err != nil {
			return err
		}
		rel := c.Git.Path
		if rel == "" {
			rel = "."
		}
		chart, err := resolveInRepo(dir, "chart.git.path", rel)
		if err != nil {
			return err
		}
		if st, serr := os.Stat(filepath.Join(chart, "Chart.yaml")); serr != nil || !st.Mode().IsRegular() {
			return pluginsdk.ValidationError("chart.git.path %q has no Chart.yaml", rel)
		}
		if err := checkNoEscapingSymlinks(chart, dir); err != nil {
			return err
		}
		r.chartRef = chart
		for _, vf := range r.in.ValuesFiles {
			full, err := resolveInRepo(dir, "values_files", filepath.Join(rel, vf))
			if err != nil {
				return err
			}
			r.valFiles = append(r.valFiles, full)
		}
	}
	return nil
}

// resolveInRepo resolves p under dir through symlinks and requires the result
// to stay inside the checkout; helm gets the absolute path.
func resolveInRepo(dir, what, p string) (string, error) {
	root, err := filepath.EvalSymlinks(dir)
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

func (r *runner) global() []string {
	args := []string{"--namespace", r.in.Namespace}
	if r.files.kubeconfig != "" {
		args = append(args, "--kubeconfig", r.files.kubeconfig)
	}
	if c := r.in.Cluster.Context; c != "" {
		args = append(args, "--kube-context", c)
	}
	return args
}

// args builds the helm argument list of the action.
func (r *runner) args() []string {
	in := r.in
	switch in.Action {
	case actionUpgrade:
		args := []string{"upgrade", "--install", in.Release, r.chartRef}
		args = append(args, r.extra...)
		args = append(args, r.global()...)
		if in.CreateNamespace {
			args = append(args, "--create-namespace")
		}
		if r.files.registry != "" {
			args = append(args, "--registry-config", r.files.registry)
		}
		for _, vf := range r.valFiles {
			args = append(args, "--values", vf)
		}
		if r.files.values != "" {
			args = append(args, "--values", r.files.values)
		}
		for _, k := range sortedKeys(in.Set) {
			args = append(args, "--set-string="+k+"="+escapeSet(in.Set[k]))
		}
		if in.atomic() {
			args = append(args, "--atomic")
		}
		if in.wait() {
			args = append(args, "--wait")
		}
		return append(args, "--timeout", in.timeout())
	case actionRollback:
		args := []string{"rollback", in.Release}
		if in.RollbackRevision > 0 {
			args = append(args, fmt.Sprint(in.RollbackRevision))
		}
		args = append(args, r.global()...)
		if in.wait() {
			args = append(args, "--wait")
		}
		return append(args, "--timeout", in.timeout())
	case actionUninstall:
		args := append([]string{"uninstall", in.Release}, r.global()...)
		if in.wait() {
			args = append(args, "--wait")
		}
		return append(args, "--timeout", in.timeout())
	}
	return r.metadataArgs()
}

// metadataArgs reads the release back with `helm get metadata`, which prints
// only name, namespace, revision, status, chart, version and app version. The
// plugin never parses (or holds) helm's release object, which carries the
// resolved values and the rendered manifests.
func (r *runner) metadataArgs() []string {
	return append(append([]string{"get", "metadata", r.in.Release}, r.global()...), "--output", "json")
}

// escapeSet escapes the characters that --set-string treats specially.
func escapeSet(v string) string {
	return strings.NewReplacer(`\`, `\\`, `,`, `\,`).Replace(v)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// run executes the action and reads the release back.
func (r *runner) run(ctx context.Context, emit pluginsdk.Emitter) (Output, error) {
	if r.in.Action != actionStatus {
		res, err := r.helm(ctx, r.args(), emit, true)
		if err != nil {
			return Output{}, err
		}
		if res.ExitCode != 0 {
			return Output{}, classify(r.in.Action, res)
		}
		if r.in.Action == actionUninstall {
			return Output{Release: r.in.Release, Namespace: r.in.Namespace, Status: "uninstalled"}, nil
		}
	}
	var meta strings.Builder
	res, err := proc.Run(ctx, proc.Spec{Dir: r.workDir, Env: r.env, Name: "helm", Args: r.metadataArgs()}, &capture{emit: emit, out: &meta})
	if err != nil {
		return Output{}, err
	}
	if res.ExitCode != 0 {
		return Output{}, classify("get metadata", res)
	}
	return parseMetadata(meta.String(), r.in)
}

// helm runs one helm command; with graceful a cancel sends SIGTERM first, so
// an atomic upgrade can roll itself back.
func (r *runner) helm(ctx context.Context, args []string, emit pluginsdk.Emitter, graceful bool) (proc.Result, error) {
	spec := proc.Spec{Dir: r.workDir, Env: r.env, Name: "helm", Args: args}
	if graceful {
		spec.Grace = terminationGrace
	}
	return proc.Run(ctx, spec, emit)
}

// terminationGrace is how long helm gets to finish after a cancel (an atomic
// upgrade rolls back). The step's own timeout must exceed helm's timeout plus
// this, or a timed-out step leaves the release in a pending state.
const terminationGrace = 60 * time.Second

// capture keeps stdout (the small JSON of `helm get metadata`) and forwards
// everything else to the log.
type capture struct {
	emit pluginsdk.Emitter
	out  *strings.Builder
}

func (c *capture) Log(stream pluginsdk.Stream, line string) {
	if stream == pluginsdk.Stdout {
		c.out.WriteString(line)
		c.out.WriteByte('\n')
		return
	}
	c.emit.Log(stream, line)
}

func (c *capture) Progress(pct float64, msg string) { c.emit.Progress(pct, msg) }

// metadata is the output of `helm get metadata --output json`.
type metadata struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Revision   int    `json:"revision"`
	Status     string `json:"status"`
	Chart      string `json:"chart"`
	Version    string `json:"version"`
	AppVersion string `json:"appVersion"`
}

func parseMetadata(stdout string, in Input) (Output, error) {
	var m metadata
	if err := json.Unmarshal([]byte(stdout), &m); err != nil || m.Name == "" {
		return Output{}, pluginsdk.NewError(pluginsdk.KindInternal, "helm get metadata printed no valid JSON (helm 3.13 or newer is required)", errors.New("unparsable helm output"))
	}
	out := Output{Release: m.Name, Namespace: m.Namespace, Revision: m.Revision, Status: m.Status, Chart: m.Chart, ChartVersion: m.Version, AppVersion: m.AppVersion}
	if out.Namespace == "" {
		out.Namespace = in.Namespace
	}
	return out, nil
}

// classify maps a failed helm command to a step error. Only helm's own "Error:"
// lines are read, and only phrases that client-go and helm use for these cases.
func classify(action string, res proc.Result) error {
	low := strings.ToLower(errorLines(res.Stderr))
	msg := fmt.Sprintf("helm %s exited with code %d", action, res.ExitCode)
	switch {
	case containsAny(low, "another operation (install/upgrade/rollback) is in progress"):
		return pluginsdk.ExitCodeError(res.ExitCode, msg+": another operation is in progress on this release (a previous step may have been interrupted; run a rollback)")
	case containsAny(low, ": unauthorized", "is forbidden: user", "authentication required", "denied: ", "requested access to the resource is denied", "you must be logged in", "invalid username/password"):
		return pluginsdk.NewError(pluginsdk.KindAuth, msg+": authentication or authorization failed", nil)
	case containsAny(low, "cluster unreachable", "no such host", "connection refused", "i/o timeout", "network is unreachable", "dial tcp", "tls handshake timeout", "x509:", "failed to verify certificate", "no route to host"):
		return pluginsdk.NewError(pluginsdk.KindConnection, msg+": the cluster or registry is unreachable", nil)
	case containsAny(low, "timed out waiting for the condition", "context deadline exceeded"):
		return pluginsdk.NewError(pluginsdk.KindTimeout, msg+": timed out waiting for the release to become ready", nil)
	}
	return pluginsdk.ExitCodeError(res.ExitCode, msg)
}

func errorLines(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "Error:") || strings.HasPrefix(t, "error:") || strings.HasPrefix(t, "Error ") {
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
