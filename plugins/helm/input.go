package helm

import (
	"bytes"
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Actions.
const (
	actionUpgrade   = "upgrade"
	actionRollback  = "rollback"
	actionUninstall = "uninstall"
	actionStatus    = "status"
)

// RepoChart is a chart in a classic Helm repository.
type RepoChart struct {
	URL     string `json:"url" jsonschema:"description=Repository URL (https),minLength=1"`
	Name    string `json:"name" jsonschema:"description=Chart name in the repository,minLength=1"`
	Version string `json:"version,omitempty" jsonschema:"description=Chart version; empty is the latest"`
}

// GitChart is a chart in a git repository, using the shared checkout.
type GitChart struct {
	URL        string `json:"url" jsonschema:"description=Repository URL (https or ssh),minLength=1"`
	Ref        string `json:"ref,omitempty" jsonschema:"description=Branch, tag or full commit SHA"`
	Path       string `json:"path,omitempty" jsonschema:"description=Chart directory in the repository,default=."`
	Credential string `json:"credential,omitempty" jsonschema:"description=secret:// ref: a token (https) or an ssh_key (ssh)"`
}

// Chart names the chart source; exactly one of oci, repo and git is set.
type Chart struct {
	OCI     string     `json:"oci,omitempty" jsonschema:"description=OCI chart reference (oci://registry.internal/charts/app)"`
	Version string     `json:"version,omitempty" jsonschema:"description=Chart version for an oci chart"`
	Repo    *RepoChart `json:"repo,omitempty"`
	Git     *GitChart  `json:"git,omitempty"`
}

// Cluster selects the cluster to deploy to; exactly one of kubeconfig and
// in_cluster is set.
type Cluster struct {
	Kubeconfig string `json:"kubeconfig,omitempty" jsonschema:"description=secret:// ref holding a kubeconfig in the field kubeconfig (secret://team/kube-prod#kubeconfig), or a generic secret with that field"`
	Context    string `json:"context,omitempty" jsonschema:"description=kubeconfig context to use"`
	InCluster  bool   `json:"in_cluster,omitempty" jsonschema:"description=Use the agent's own service account. Needs the agent setting allow_helm_in_cluster."`
}

// RegistryCredential logs in to one OCI registry for pulling a chart.
type RegistryCredential struct {
	Registry   string `json:"registry" jsonschema:"description=Registry host,minLength=1"`
	Credential string `json:"credential" jsonschema:"description=secret:// ref of a password credential,minLength=1"`
}

// Input is the helm plugin input.
type Input struct {
	Action              string               `json:"action,omitempty" jsonschema:"description=upgrade installs or upgrades (default) rollback uninstall or status,enum=upgrade,enum=rollback,enum=uninstall,enum=status,default=upgrade"`
	Release             string               `json:"release" jsonschema:"description=Release name,minLength=1"`
	Namespace           string               `json:"namespace" jsonschema:"description=Kubernetes namespace,minLength=1"`
	CreateNamespace     bool                 `json:"create_namespace,omitempty" jsonschema:"description=Create the namespace if it does not exist (upgrade)"`
	Chart               *Chart               `json:"chart,omitempty" jsonschema:"description=Chart source; required for upgrade"`
	Values              map[string]any       `json:"values,omitempty" jsonschema:"description=Chart values. A string value of the form secret://... is resolved from the secret bundle."`
	ValuesFiles         []string             `json:"values_files,omitempty" jsonschema:"description=Values files inside a git chart source, relative to the chart directory"`
	Set                 map[string]string    `json:"set,omitempty" jsonschema:"description=Single values as strings (--set-string). Not for secrets: they are visible on the command line; use values."`
	Cluster             *Cluster             `json:"cluster,omitempty" jsonschema:"description=Target cluster"`
	Atomic              *bool                `json:"atomic,omitempty" jsonschema:"description=Roll back a failed upgrade by itself,default=true"`
	Wait                *bool                `json:"wait,omitempty" jsonschema:"description=Wait until the resources are ready,default=true"`
	Timeout             string               `json:"timeout,omitempty" jsonschema:"description=Helm timeout such as 10m,default=10m"`
	RegistryCredentials []RegistryCredential `json:"registry_credentials,omitempty" jsonschema:"description=Logins for OCI chart registries"`
	RollbackRevision    int                  `json:"rollback_revision,omitempty" jsonschema:"description=Revision to roll back to; default is the previous one,minimum=0"`
}

// Output is the helm plugin output.
type Output struct {
	Release      string `json:"release"`
	Namespace    string `json:"namespace"`
	Revision     int    `json:"revision" jsonschema:"description=Release revision after the action; 0 after uninstall"`
	Status       string `json:"status" jsonschema:"description=Release status such as deployed or failed or uninstalled"`
	Chart        string `json:"chart,omitempty"`
	ChartVersion string `json:"chart_version,omitempty"`
	AppVersion   string `json:"app_version,omitempty"`
}

var (
	releaseRe   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,51}[a-z0-9])?$`)
	namespaceRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	timeoutRe   = regexp.MustCompile(`^[0-9]{1,4}(s|m|h)$`)
	versionRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
	chartNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	ociRe       = regexp.MustCompile(`^oci://[a-z0-9][a-z0-9.:/_-]*$`)
	setKeyRe    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.\[\]-]*$`)
	registryRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*(:[0-9]{1,5})?$`)
	ctxNameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@_.:/-]*$`)
)

func decode(raw json.RawMessage) (Input, error) {
	var in Input
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, pluginsdk.ValidationError("invalid input: %v", err)
	}
	if in.Action == "" {
		in.Action = actionUpgrade
	}
	return in, in.validate()
}

func (in Input) validate() error {
	switch in.Action {
	case actionUpgrade, actionRollback, actionUninstall, actionStatus:
	default:
		return pluginsdk.ValidationError("action must be upgrade, rollback, uninstall or status")
	}
	switch {
	case !releaseRe.MatchString(in.Release):
		return pluginsdk.ValidationError("release must be a valid release name (lowercase letters, digits and '-', at most 53 characters)")
	case !namespaceRe.MatchString(in.Namespace):
		return pluginsdk.ValidationError("namespace must be a valid Kubernetes namespace name")
	case in.Cluster == nil:
		return pluginsdk.ValidationError("cluster is required: a kubeconfig or in_cluster")
	case in.Timeout != "" && !timeoutRe.MatchString(in.Timeout):
		return pluginsdk.ValidationError("timeout must look like 30s, 10m or 1h")
	case in.RollbackRevision < 0:
		return pluginsdk.ValidationError("rollback_revision must not be negative")
	case in.RollbackRevision > 0 && in.Action != actionRollback:
		return pluginsdk.ValidationError("rollback_revision needs action rollback")
	case in.Action == actionUpgrade && in.atomic() && in.Wait != nil && !*in.Wait:
		return pluginsdk.ValidationError("wait=false needs atomic=false: helm's --atomic always waits")
	}
	if err := in.validateCluster(); err != nil {
		return err
	}
	if in.Action != actionUpgrade {
		if in.Chart != nil || len(in.Values) > 0 || len(in.ValuesFiles) > 0 || len(in.Set) > 0 || in.CreateNamespace || len(in.RegistryCredentials) > 0 {
			return pluginsdk.ValidationError("chart, values, values_files, set, create_namespace and registry_credentials are only for action upgrade")
		}
		return nil
	}
	if err := in.validateChart(); err != nil {
		return err
	}
	for k, v := range in.Set {
		switch {
		case !setKeyRe.MatchString(k):
			return pluginsdk.ValidationError("set key %q is not valid", k)
		case strings.HasPrefix(v, "secret://"):
			return pluginsdk.ValidationError("set %q holds a secret reference: set values are visible on the command line; put it in values", k)
		case strings.ContainsAny(v, "\x00\n\r"):
			return pluginsdk.ValidationError("set %q must be single-line text", k)
		}
	}
	seen := map[string]bool{}
	for _, rc := range in.RegistryCredentials {
		if !registryRe.MatchString(rc.Registry) || !strings.HasPrefix(rc.Credential, "secret://") || seen[rc.Registry] {
			return pluginsdk.ValidationError("registry_credentials needs a unique registry host and a secret:// credential for each entry")
		}
		seen[rc.Registry] = true
	}
	for _, f := range in.ValuesFiles {
		// Relative to the chart directory; "../shared.yaml" is fine as long as it
		// stays inside the checkout, which resolveInRepo enforces.
		if f == "" || strings.ContainsAny(f, "\x00\n\r") || path.IsAbs(f) || strings.HasPrefix(f, "-") {
			return pluginsdk.ValidationError("values_files must be relative paths")
		}
	}
	if len(in.ValuesFiles) > 0 && (in.Chart.Git == nil) {
		return pluginsdk.ValidationError("values_files are only for a git chart source")
	}
	return nil
}

func (in Input) validateCluster() error {
	c := in.Cluster
	if (c.Kubeconfig == "") == !c.InCluster {
		return pluginsdk.ValidationError("cluster needs exactly one of kubeconfig and in_cluster")
	}
	if c.Kubeconfig != "" && !strings.HasPrefix(c.Kubeconfig, "secret://") {
		return pluginsdk.ValidationError("cluster.kubeconfig must be a secret:// ref")
	}
	if c.Context != "" && (!ctxNameRe.MatchString(c.Context) || c.InCluster) {
		return pluginsdk.ValidationError("cluster.context is not valid (and is only for a kubeconfig)")
	}
	return nil
}

func (in Input) validateChart() error {
	c := in.Chart
	if c == nil {
		return pluginsdk.ValidationError("chart is required for upgrade")
	}
	n := 0
	if c.OCI != "" {
		n++
	}
	if c.Repo != nil {
		n++
	}
	if c.Git != nil {
		n++
	}
	if n != 1 {
		return pluginsdk.ValidationError("chart needs exactly one of oci, repo and git")
	}
	if c.Version != "" && c.OCI == "" {
		return pluginsdk.ValidationError("chart.version is for an oci chart; repo has its own version")
	}
	if c.Version != "" && !versionRe.MatchString(c.Version) {
		return pluginsdk.ValidationError("chart.version is not valid")
	}
	switch {
	case c.OCI != "":
		// A colon is allowed only in the host, as the port: a tag belongs in version.
		rest := strings.TrimPrefix(c.OCI, "oci://")
		_, path, _ := strings.Cut(rest, "/")
		if !ociRe.MatchString(c.OCI) || strings.Contains(c.OCI, "..") || strings.HasSuffix(c.OCI, "/") || strings.Contains(path, ":") || path == "" {
			return pluginsdk.ValidationError("chart.oci must look like oci://registry.internal/charts/app, without a tag (use version)")
		}
	case c.Repo != nil:
		if !strings.HasPrefix(c.Repo.URL, "https://") || strings.ContainsAny(c.Repo.URL, " \x00\n\r@") || !chartNameRe.MatchString(c.Repo.Name) ||
			(c.Repo.Version != "" && !versionRe.MatchString(c.Repo.Version)) {
			return pluginsdk.ValidationError("chart.repo needs an https url without credentials, a chart name and an optional version")
		}
	case c.Git != nil:
		if c.Git.URL == "" {
			return pluginsdk.ValidationError("chart.git.url is required")
		}
		if err := safeRepoPath("chart.git.path", c.Git.Path, true); err != nil {
			return err
		}
	}
	return nil
}

// safeRepoPath requires a relative path inside the checkout; allowDot permits ".".
func safeRepoPath(what, p string, allowDot bool) error {
	if p == "" {
		return nil
	}
	clean := path.Clean(p)
	if strings.ContainsAny(p, "\x00\n\r") || path.IsAbs(p) || strings.HasPrefix(p, "-") || clean == ".." || strings.HasPrefix(clean, "../") || (clean == "." && !allowDot) {
		return pluginsdk.ValidationError("%s must be a relative path inside the repository", what)
	}
	return nil
}

func (in Input) atomic() bool { return in.Atomic == nil || *in.Atomic }
func (in Input) wait() bool   { return in.Wait == nil || *in.Wait }
func (in Input) timeout() string {
	if in.Timeout == "" {
		return "10m"
	}
	return in.Timeout
}
