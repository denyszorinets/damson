package ansible

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/internal/proc"
)

// runOutcome is what a run produced.
type runOutcome struct {
	stats    map[string]HostStats
	exitCode int
}

// environment is the minimal environment of ansible commands. The agent's own
// environment is not passed on, except what ansible needs to find its tools and
// reach a Galaxy mirror or proxy.
func environment(workDir string) []string {
	home := filepath.Join(workDir, "home")
	// The agent's HOME is kept so a user-site Python install of ansible (pip
	// install --user) still imports; ansible itself writes only under ANSIBLE_HOME
	// and ANSIBLE_LOCAL_TEMP, and ssh ignores ~ (it runs with -F /dev/null).
	userHome := os.Getenv("HOME")
	if userHome == "" {
		userHome = home
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + userHome,
		"ANSIBLE_HOME=" + filepath.Join(home, ".ansible"),
		"ANSIBLE_LOCAL_TEMP=" + filepath.Join(home, "tmp"),
		"ANSIBLE_NOCOLOR=1",
		"ANSIBLE_FORCE_COLOR=0",
		"ANSIBLE_RETRY_FILES_ENABLED=0",
		"ANSIBLE_HOST_KEY_CHECKING=True",
		"ANSIBLE_ROLES_PATH=" + filepath.Join(workDir, "galaxy", "roles"),
		"ANSIBLE_COLLECTIONS_PATH=" + filepath.Join(workDir, "galaxy", "collections"),
		"ANSIBLE_CALLBACK_PLUGINS=" + filepath.Join(workDir, "callbacks"),
		"ANSIBLE_CALLBACKS_ENABLED=damson_events",
		"DAMSON_EVENTS_FILE=" + filepath.Join(workDir, "creds", "events.jsonl"),
		// Environment variables beat the repository's ansible.cfg, so these
		// settings cannot be undone from the checkout: no ssh master that outlives
		// a cancel (it escapes the process group), no user ssh config, no log file.
		"ANSIBLE_SSH_ARGS=-F /dev/null -o ControlMaster=no -o ControlPath=none",
		"ANSIBLE_LOG_PATH=/dev/null",
		"PYTHONUNBUFFERED=1",
		"LC_ALL=C.UTF-8",
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "ANSIBLE_GALAXY_"), k == "SSL_CERT_FILE", k == "SSL_CERT_DIR", k == "REQUESTS_CA_BUNDLE",
			k == "HTTPS_PROXY", k == "https_proxy", k == "HTTP_PROXY", k == "http_proxy", k == "NO_PROXY", k == "no_proxy", k == "TMPDIR":
			env = append(env, kv)
		}
	}
	return env
}

// runPlaybook installs requirements, then runs the playbook, and reads the
// callback's recap.
func runPlaybook(ctx context.Context, workDir, repoDir string, in Input, pl plan, extra, vault string, emit pluginsdk.Emitter) (runOutcome, error) {
	for _, d := range []string{"home", "home/tmp", "callbacks", "galaxy/roles", "galaxy/collections"} {
		if err := os.MkdirAll(filepath.Join(workDir, d), 0o700); err != nil {
			return runOutcome{exitCode: -1}, pluginsdk.NewError(pluginsdk.KindInternal, "preparing the work directory failed", err)
		}
	}
	if err := os.WriteFile(filepath.Join(workDir, "callbacks", "damson_events.py"), callbackSource, 0o600); err != nil {
		return runOutcome{exitCode: -1}, pluginsdk.NewError(pluginsdk.KindInternal, "writing the callback plugin failed", err)
	}
	env := environment(workDir)
	if in.Requirements != "" {
		if _, err := os.Stat(filepath.Join(repoDir, in.Requirements)); err != nil {
			return runOutcome{exitCode: -1}, pluginsdk.ValidationError("requirements file %q is not in the repository", in.Requirements)
		}
		emit.Log(pluginsdk.System, "installing requirements from "+in.Requirements)
		code, err := runCommand(ctx, repoDir, env, emit, "ansible-galaxy", "install", "-r", in.Requirements)
		if err != nil {
			return runOutcome{exitCode: -1}, err
		}
		if code != 0 {
			return runOutcome{exitCode: code}, pluginsdk.ExitCodeError(code, fmt.Sprintf("ansible-galaxy exited with code %d", code))
		}
	}
	code, err := runCommand(ctx, repoDir, env, emit, "ansible-playbook", playbookArgs(in, pl, extra, vault)...)
	if err != nil {
		return runOutcome{exitCode: code, stats: readStats(filepath.Join(workDir, "creds", "events.jsonl"))}, err
	}
	return runOutcome{exitCode: code, stats: readStats(filepath.Join(workDir, "creds", "events.jsonl"))}, nil
}

// playbookArgs builds the ansible-playbook argument list. No secret is in it:
// credentials and variables are in files.
func playbookArgs(in Input, pl plan, extra, vault string) []string {
	args := []string{"-i", pl.inventory}
	if extra != "" {
		args = append(args, "-e", "@"+extra)
	}
	if vault != "" {
		args = append(args, "--vault-password-file", vault)
	}
	if in.Limit != "" {
		args = append(args, "--limit="+in.Limit)
	}
	if len(in.Tags) > 0 {
		args = append(args, "--tags", strings.Join(in.Tags, ","))
	}
	if len(in.SkipTags) > 0 {
		args = append(args, "--skip-tags", strings.Join(in.SkipTags, ","))
	}
	if in.Check {
		args = append(args, "--check")
	}
	if in.Diff {
		args = append(args, "--diff")
	}
	if in.Forks > 0 {
		args = append(args, "--forks", strconv.Itoa(in.Forks))
	}
	if in.Verbosity > 0 {
		args = append(args, "-"+strings.Repeat("v", in.Verbosity))
	}
	return append(args, "--", in.Playbook)
}

// runCommand runs one command in dir and returns its exit code.
func runCommand(ctx context.Context, dir string, env []string, emit pluginsdk.Emitter, name string, args ...string) (int, error) {
	res, err := proc.Run(ctx, proc.Spec{Dir: dir, Env: env, Name: name, Args: args}, emit)
	return res.ExitCode, err
}

// readStats returns the recap of the callback's last stats event, nil if none.
func readStats(path string) map[string]HostStats {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var stats map[string]HostStats
	br := bufio.NewReader(f)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var ev struct {
				Event string               `json:"event"`
				Hosts map[string]HostStats `json:"hosts"`
			}
			if json.Unmarshal(line, &ev) == nil && ev.Event == "stats" {
				stats = ev.Hosts
			}
		}
		if err != nil {
			return stats
		}
	}
}

// outcomeError maps the exit of ansible-playbook to a step error: unreachable
// hosts are connection errors, failed tasks exit_code errors.
func outcomeError(rr runOutcome) error {
	if rr.exitCode == 0 {
		return nil
	}
	var unreachable, failed []string
	for _, h := range sortedHosts(rr.stats) {
		s := rr.stats[h]
		if s.Unreachable > 0 {
			unreachable = append(unreachable, h)
		} else if s.Failed > 0 {
			failed = append(failed, h)
		}
	}
	switch {
	case len(unreachable) > 0 && len(failed) == 0:
		return pluginsdk.NewError(pluginsdk.KindConnection, "unreachable hosts: "+strings.Join(unreachable, ", "), nil)
	case len(failed) > 0:
		return pluginsdk.ExitCodeError(rr.exitCode, "failed hosts: "+strings.Join(failed, ", "))
	}
	return pluginsdk.ExitCodeError(rr.exitCode, fmt.Sprintf("ansible-playbook exited with code %d", rr.exitCode))
}

func sortedHosts(m map[string]HostStats) []string {
	out := make([]string, 0, len(m))
	for h := range m {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}
