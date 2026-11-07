// Package helmtest is a test fixture for the helm plugin: a fake helm that
// records every call and answers from files, and a local git repository with a
// chart. It needs a POSIX sh and git, and is used only by tests.
package helmtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const script = `#!/bin/sh
d=$(dirname "$0")
mkdir -p "$d/calls"
n=$(ls "$d/calls" | grep -c '\.txt$')
sub="$1"
printf '%s\n' "$@" > "$d/calls/$n-$sub.txt"
env > "$d/calls/$n-$sub.env"
prev=""
for a in "$@"; do
  case "$prev" in
    --kubeconfig) cp "$a" "$d/kubeconfig.copy" ;;
    --values) cp "$a" "$d/values-copy-$(basename "$a")" ;;
    --registry-config) cp "$a" "$d/registry.copy" ;;
  esac
  prev="$a"
done
[ -f "$d/stdout_$sub" ] && cat "$d/stdout_$sub"
[ -f "$d/stderr_$sub" ] && cat "$d/stderr_$sub" >&2
case "$*" in *block*) sleep 60;; esac
code=0
[ -f "$d/exit_$sub" ] && code=$(cat "$d/exit_$sub")
exit "$code"
`

// Release returns a helm release JSON.
func Release(name, ns string, revision int, status string) string {
	return `{"name":"` + name + `","namespace":"` + ns + `","version":` + itoa(revision) +
		`,"info":{"status":"` + status + `"},"chart":{"metadata":{"name":"app","version":"2.3.0","appVersion":"1.9.1"}}}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

// Fake is the installed fake helm.
type Fake struct{ Dir string }

// Install puts a fake helm first on PATH for the test.
func Install(t *testing.T) *Fake {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "helm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
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

// Stdout sets what a subcommand ("upgrade", "status", ...) prints.
func (f *Fake) Stdout(t *testing.T, sub, text string) { f.write(t, "stdout_"+sub, text) }

// Stderr sets what a subcommand prints to stderr.
func (f *Fake) Stderr(t *testing.T, sub, text string) { f.write(t, "stderr_"+sub, text) }

// Exit sets the exit code of a subcommand.
func (f *Fake) Exit(t *testing.T, sub, code string) { f.write(t, "exit_"+sub, code) }

// Calls returns the argument lists of the calls to a subcommand, in order.
func (f *Fake) Calls(t *testing.T, sub string) [][]string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(f.Dir, "calls", "*-"+sub+".txt"))
	var out [][]string
	for _, name := range files {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, strings.Split(strings.TrimRight(string(b), "\n"), "\n"))
	}
	return out
}

// CallEnv returns the recorded environment of the first call to sub.
func (f *Fake) CallEnv(t *testing.T, sub string) string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(f.Dir, "calls", "*-"+sub+".env"))
	if len(files) == 0 {
		t.Fatalf("no recorded %s call", sub)
	}
	b, _ := os.ReadFile(files[0])
	return string(b)
}

// Read returns a file the fake recorded ("kubeconfig.copy", "values-copy-values.json").
func (f *Fake) Read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.Dir, name))
	if err != nil {
		t.Fatalf("the fake recorded no %s: %v", name, err)
	}
	return string(b)
}

// Repo creates a git repository with the chart charts/app (Chart.yaml and
// values-prod.yaml) and two symlinks that leave the repository, and returns its
// file:// URL (which needs the plugin's allow_insecure_git setting).
func Repo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	for name, body := range map[string]string{
		"charts/app/Chart.yaml":       "apiVersion: v2\nname: app\nversion: 2.3.0\n",
		"charts/app/values-prod.yaml": "replicas: 3\n",
		"charts/nochart/README":       "no chart here\n",
	} {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("..", filepath.Join(dir, "linkchart")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "charts", "app", "values-evil.yaml")); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "chart")
	return "file://" + dir
}
