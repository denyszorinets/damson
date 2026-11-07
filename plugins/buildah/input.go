package buildah

import (
	"bytes"
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Repo is the git repository that holds the build context.
type Repo struct {
	URL        string `json:"url" jsonschema:"description=Repository URL (https or ssh),minLength=1"`
	Ref        string `json:"ref,omitempty" jsonschema:"description=Branch, tag or full commit SHA; empty is the default branch"`
	Credential string `json:"credential,omitempty" jsonschema:"description=secret:// ref: a token (https; field token) or an ssh_key (ssh)"`
	Submodules bool   `json:"submodules,omitempty" jsonschema:"description=Also clone submodules"`
}

// RegistryCredential logs in to one registry for pulling base images and pushing.
type RegistryCredential struct {
	Registry   string `json:"registry" jsonschema:"description=Registry host, with the port if it is not the default (registry.internal or localhost:5000),minLength=1"`
	Credential string `json:"credential" jsonschema:"description=secret:// ref of a password credential (username and password),minLength=1"`
}

// Cache selects registry-based layer caching.
type Cache struct {
	From string `json:"from,omitempty" jsonschema:"description=Repository to read cached layers from (--cache-from)"`
	To   string `json:"to,omitempty" jsonschema:"description=Repository to push cached layers to (--cache-to)"`
}

// Input is the buildah plugin input.
type Input struct {
	Repo                Repo                 `json:"repo"`
	Context             string               `json:"context,omitempty" jsonschema:"description=Build context directory in the repository,default=."`
	Containerfile       string               `json:"containerfile,omitempty" jsonschema:"description=Containerfile path in the repository; default Containerfile, falling back to Dockerfile"`
	Image               string               `json:"image" jsonschema:"description=Image repository to build and push (registry.internal/team/app) without tag or digest,minLength=1"`
	Tags                []string             `json:"tags,omitempty" jsonschema:"description=Tags to apply; default is the short commit SHA"`
	BuildArgs           map[string]string    `json:"build_args,omitempty" jsonschema:"description=Build arguments. They are visible in the build output and the image history: use build_secrets for secrets."`
	Labels              map[string]string    `json:"labels,omitempty" jsonschema:"description=Image labels"`
	Target              string               `json:"target,omitempty" jsonschema:"description=Build stage to stop at"`
	BuildSecrets        map[string]string    `json:"build_secrets,omitempty" jsonschema:"description=Map of secret id to secret:// ref, mounted with RUN --mount=type=secret,id=<id>"`
	RegistryCredentials []RegistryCredential `json:"registry_credentials,omitempty" jsonschema:"description=Registry logins for pulling base images and pushing"`
	Cache               *Cache               `json:"cache,omitempty"`
	Push                *bool                `json:"push,omitempty" jsonschema:"description=Push the image after the build,default=true"`
	TLSVerify           *bool                `json:"tls_verify,omitempty" jsonschema:"description=Verify registry certificates. false needs the agent setting allow_insecure_registry,default=true"`
	Platform            string               `json:"platform,omitempty" jsonschema:"description=A single target platform such as linux/amd64"`
}

// Output is the buildah plugin output.
type Output struct {
	Image    string   `json:"image" jsonschema:"description=The image repository"`
	Digest   string   `json:"digest,omitempty" jsonschema:"description=Registry digest (sha256:...) of the pushed image; empty when push is false"`
	ImageRef string   `json:"image_ref,omitempty" jsonschema:"description=<image>@<digest>; prefer it for deploys. Empty when push is false"`
	Tags     []string `json:"tags" jsonschema:"description=The tags that were applied"`
	Commit   string   `json:"commit" jsonschema:"description=The resolved commit SHA of the checkout"`
}

var (
	imageRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*$`)
	cacheRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*$`)
	tagRe      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	argNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	labelKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	targetRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	platformRe = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+(/[a-z0-9]+)?$`)
	secretIDRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	registryRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*(:[0-9]{1,5})?$`)
)

func decode(raw json.RawMessage) (Input, error) {
	var in Input
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, pluginsdk.ValidationError("invalid input: %v", err)
	}
	return in, in.validate()
}

func (in Input) validate() error {
	if in.Repo.URL == "" {
		return pluginsdk.ValidationError("repo.url is required")
	}
	if err := validateImage("image", in.Image); err != nil {
		return err
	}
	for _, t := range in.Tags {
		if !tagRe.MatchString(t) {
			return pluginsdk.ValidationError("tag %q is not valid", t)
		}
	}
	if err := safeRepoPath("context", in.Context, true); err != nil {
		return err
	}
	if in.Containerfile != "" {
		if err := safeRepoPath("containerfile", in.Containerfile, false); err != nil {
			return err
		}
	}
	for k, v := range in.BuildArgs {
		switch {
		case !argNameRe.MatchString(k):
			return pluginsdk.ValidationError("build_args name %q is not valid", k)
		case strings.HasPrefix(v, "secret://"):
			return pluginsdk.ValidationError("build_args %q holds a secret reference: build arguments are visible in the build output and the image history; use build_secrets", k)
		case strings.ContainsAny(v, "\x00\n\r"):
			return pluginsdk.ValidationError("build_args %q must be single-line text", k)
		}
	}
	for k, v := range in.Labels {
		if !labelKeyRe.MatchString(k) || strings.ContainsAny(v, "\x00\n\r") || strings.HasPrefix(v, "secret://") {
			return pluginsdk.ValidationError("label %q is not valid (single-line text, no secret reference)", k)
		}
	}
	if in.Target != "" && !targetRe.MatchString(in.Target) {
		return pluginsdk.ValidationError("target is not valid")
	}
	if in.Platform != "" && !platformRe.MatchString(in.Platform) {
		return pluginsdk.ValidationError("platform must look like linux/amd64")
	}
	for id, ref := range in.BuildSecrets {
		if !secretIDRe.MatchString(id) || !strings.HasPrefix(ref, "secret://") {
			return pluginsdk.ValidationError("build_secrets %q must map a secret id to a secret:// ref", id)
		}
	}
	seen := map[string]bool{}
	for _, rc := range in.RegistryCredentials {
		if !registryRe.MatchString(rc.Registry) || !strings.HasPrefix(rc.Credential, "secret://") || seen[rc.Registry] {
			return pluginsdk.ValidationError("registry_credentials needs a unique registry host and a secret:// credential for each entry")
		}
		seen[rc.Registry] = true
	}
	if in.Cache != nil {
		for _, c := range []string{in.Cache.From, in.Cache.To} {
			if c != "" && (!cacheRe.MatchString(c) || strings.Contains(c, "..")) {
				return pluginsdk.ValidationError("cache.from and cache.to must be image repositories")
			}
		}
	}
	return nil
}

// validateImage requires a repository name: no scheme, no digest and no tag.
func validateImage(what, image string) error {
	bad := image == "" || !imageRe.MatchString(image) || strings.Contains(image, "..") || strings.Contains(image, "//") || strings.HasSuffix(image, "/")
	segs := strings.Split(image, "/")
	// A colon is allowed only in the first segment, as the registry port.
	for i, s := range segs {
		if strings.Contains(s, ":") && (i != 0 || len(segs) == 1) {
			bad = true
		}
	}
	if bad {
		return pluginsdk.ValidationError("%s must be an image repository such as registry.internal/team/app, without a scheme, tag or digest (use tags)", what)
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

func (in Input) push() bool      { return in.Push == nil || *in.Push }
func (in Input) tlsVerify() bool { return in.TLSVerify == nil || *in.TLSVerify }
