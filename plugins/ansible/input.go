package ansible

import (
	"bytes"
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Repo is the git repository that holds the playbook.
type Repo struct {
	URL        string `json:"url" jsonschema:"description=Repository URL (https or ssh),minLength=1"`
	Ref        string `json:"ref,omitempty" jsonschema:"description=Branch, tag or full commit SHA; empty is the default branch"`
	Credential string `json:"credential,omitempty" jsonschema:"description=secret:// ref: a token (https; field token) or an ssh_key (ssh)"`
	Submodules bool   `json:"submodules,omitempty" jsonschema:"description=Also clone submodules"`
}

// Input is the ansible plugin input.
type Input struct {
	Repo          Repo           `json:"repo"`
	Playbook      string         `json:"playbook" jsonschema:"description=Path of the playbook in the repository,minLength=1"`
	Requirements  string         `json:"requirements,omitempty" jsonschema:"description=Path of a requirements.yml in the repository; roles and collections are installed from the configured Galaxy or mirror"`
	Hosts         []string       `json:"hosts" jsonschema:"description=Names of the inventory nodes to run on. Usually ${{ targets.map(t, t.name) }}."`
	ExtraVars     map[string]any `json:"extra_vars,omitempty" jsonschema:"description=Extra variables. A string value of the form secret://... is resolved from the secret bundle."`
	Limit         string         `json:"limit,omitempty" jsonschema:"description=Ansible --limit pattern; it can only narrow the hosts"`
	Tags          []string       `json:"tags,omitempty" jsonschema:"description=Run only tasks with these tags"`
	SkipTags      []string       `json:"skip_tags,omitempty" jsonschema:"description=Skip tasks with these tags"`
	Check         bool           `json:"check,omitempty" jsonschema:"description=Dry run (--check)"`
	Diff          bool           `json:"diff,omitempty" jsonschema:"description=Show changes (--diff)"`
	Forks         int            `json:"forks,omitempty" jsonschema:"description=Parallel hosts (--forks),minimum=1,maximum=200"`
	Verbosity     int            `json:"verbosity,omitempty" jsonschema:"description=Verbosity 0 to 4 (-v to -vvvv),minimum=0,maximum=4"`
	VaultPassword string         `json:"vault_password,omitempty" jsonschema:"description=secret:// ref of the Ansible Vault password"`
	Credential    string         `json:"credential,omitempty" jsonschema:"description=Default credential secret:// ref for hosts whose node has none"`
}

// HostStats is the recap of one host.
type HostStats struct {
	OK          int `json:"ok"`
	Changed     int `json:"changed"`
	Failed      int `json:"failed"`
	Unreachable int `json:"unreachable"`
	Skipped     int `json:"skipped"`
}

// Output is the ansible plugin output.
type Output struct {
	Stats        map[string]HostStats `json:"stats" jsonschema:"description=Recap per host"`
	FailedHosts  []string             `json:"failed_hosts" jsonschema:"description=Hosts with failed tasks or that were unreachable"`
	ChangedHosts []string             `json:"changed_hosts" jsonschema:"description=Hosts where a task changed something"`
	Commit       string               `json:"commit" jsonschema:"description=The resolved commit SHA of the checkout"`
	ExitCode     int                  `json:"exit_code" jsonschema:"description=Exit code of ansible-playbook"`
}

var (
	tagRe   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:-]*$`)
	limitRe = regexp.MustCompile(`^[A-Za-z0-9_.:!&*\[\],@-]+$`)
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
	switch {
	case in.Repo.URL == "":
		return pluginsdk.ValidationError("repo.url is required")
	case in.Playbook == "":
		return pluginsdk.ValidationError("playbook is required")
	case in.Hosts == nil:
		return pluginsdk.ValidationError("hosts is required (it may be an empty list)")
	case in.Forks < 0 || in.Forks > 200:
		return pluginsdk.ValidationError("forks must be 1 to 200")
	case in.Verbosity < 0 || in.Verbosity > 4:
		return pluginsdk.ValidationError("verbosity must be 0 to 4")
	}
	if err := safeRepoPath("playbook", in.Playbook); err != nil {
		return err
	}
	if in.Requirements != "" {
		if err := safeRepoPath("requirements", in.Requirements); err != nil {
			return err
		}
	}
	if in.Limit != "" && !limitRe.MatchString(in.Limit) {
		return pluginsdk.ValidationError("limit has unsupported characters")
	}
	for _, t := range append(append([]string{}, in.Tags...), in.SkipTags...) {
		if !tagRe.MatchString(t) {
			return pluginsdk.ValidationError("tag %q is not valid", t)
		}
	}
	for k := range in.ExtraVars {
		if !varNameRe.MatchString(k) {
			return pluginsdk.ValidationError("extra_vars name %q is not a valid variable name", k)
		}
	}
	return nil
}

var varNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// safeRepoPath requires a relative path inside the repository checkout.
func safeRepoPath(what, p string) error {
	clean := path.Clean(p)
	if strings.ContainsAny(p, "\x00\n\r") || path.IsAbs(p) || strings.HasPrefix(p, "-") || clean == ".." || strings.HasPrefix(clean, "../") || clean == "." {
		return pluginsdk.ValidationError("%s must be a relative path inside the repository", what)
	}
	return nil
}
