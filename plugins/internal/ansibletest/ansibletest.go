// Package ansibletest is a test fixture for the ansible plugin: fake
// ansible-playbook and ansible-galaxy programs that record how they were
// called and answer from files, and a local git repository with a playbook. It
// needs a POSIX sh and git, and is used only by tests.
package ansibletest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const playbookScript = `#!/bin/sh
d=$(dirname "$0")
printf '%s\n' "$@" > "$d/args.txt"
pwd > "$d/cwd.txt"
env > "$d/env.txt"
inv=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-i" ]; then inv="$a"; fi
  prev="$a"
done
cp "$inv" "$d/inventory.yml"
creds=$(dirname "$inv")
ls "$creds" > "$d/creds-listing.txt"
for f in extra_vars.json vault_password; do
  [ -f "$creds/$f" ] && cp "$creds/$f" "$d/$f"
done
[ -f "$d/kh.append" ] && cat "$d/kh.append" >> "$creds/known_hosts"
[ -f "$d/events.src" ] && cat "$d/events.src" >> "$DAMSON_EVENTS_FILE"
[ -f "$d/stdout.txt" ] && cat "$d/stdout.txt"
for a in "$@"; do [ "$a" = "block.yml" ] && sleep 60; done
code=0
[ -f "$d/exit_code" ] && code=$(cat "$d/exit_code")
exit "$code"
`

const galaxyScript = `#!/bin/sh
d=$(dirname "$0")
printf '%s\n' "$@" > "$d/galaxy-args.txt"
exit 0
`

// Fake is the installed fake ansible.
type Fake struct{ Dir string }

// Install puts fake ansible-playbook and ansible-galaxy first on PATH for the
// test.
func Install(t *testing.T) *Fake {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"ansible-playbook": playbookScript, "ansible-galaxy": galaxyScript} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &Fake{Dir: dir}
}

func (f *Fake) write(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.Dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Events sets the lines the fake appends to the callback events file.
func (f *Fake) Events(t *testing.T, lines ...string) {
	f.write(t, "events.src", strings.Join(lines, "\n")+"\n")
}

// Stats sets a recap event for the given host stats JSON ("{...}" per host).
func (f *Fake) Stats(t *testing.T, hostsJSON string) {
	f.Events(t, `{"event":"play","name":"p"}`, `{"event":"stats","hosts":`+hostsJSON+`}`)
}

// ExitCode sets the exit code of ansible-playbook.
func (f *Fake) ExitCode(t *testing.T, code string) { f.write(t, "exit_code", code) }

// Stdout sets text the fake prints.
func (f *Fake) Stdout(t *testing.T, s string) { f.write(t, "stdout.txt", s) }

// AppendKnownHosts sets lines the fake appends to the run's known_hosts file,
// as ssh would for a trust-on-first-use host.
func (f *Fake) AppendKnownHosts(t *testing.T, s string) { f.write(t, "kh.append", s) }

// A playbook named block.yml makes the fake sleep until it is killed.

// Read returns a file the fake recorded ("args.txt", "inventory.yml", ...).
func (f *Fake) Read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.Dir, name))
	if err != nil {
		t.Fatalf("the fake recorded no %s: %v", name, err)
	}
	return string(b)
}

// Exists reports whether the fake recorded the file.
func (f *Fake) Exists(name string) bool {
	_, err := os.Stat(filepath.Join(f.Dir, name))
	return err == nil
}

// Repo creates a git repository with site.yml and requirements.yml and returns
// its file:// URL (which needs the plugin's allow_insecure_git setting) and the
// commit.
func Repo(t *testing.T) (url, commit string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	for name, body := range map[string]string{
		"site.yml":         "- hosts: all\n  gather_facts: false\n  tasks:\n    - ansible.builtin.debug: {msg: hello}\n",
		"requirements.yml": "---\nroles: []\ncollections: []\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-q", "-m", "playbook")
	return "file://" + dir, run("rev-parse", "HEAD")
}
