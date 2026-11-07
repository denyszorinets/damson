// Package buildahtest is a test fixture for the buildah plugin: a fake buildah
// that records every call and answers from files, and a local git repository
// with a Containerfile. It needs a POSIX sh and git, and is used only by tests.
package buildahtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const script = `#!/bin/sh
d=$(dirname "$0")
n=$(ls "$d/calls" 2>/dev/null | wc -l)
mkdir -p "$d/calls"
# skip the global options to find the subcommand
sub=""
prev=""
for a in "$@"; do
  case "$prev" in --root) mkdir -p "$a";; esac
  case "$prev" in --root|--runroot) prev=""; continue;; esac
  case "$a" in --root|--runroot) prev="$a"; continue;; esac
  sub="$a"; break
done
printf '%s\n' "$@" > "$d/calls/$n-$sub.txt"
env > "$d/calls/$n-$sub.env"
pwd > "$d/calls/$n-$sub.cwd"
if [ -n "$REGISTRY_AUTH_FILE" ] && [ -f "$REGISTRY_AUTH_FILE" ]; then cp "$REGISTRY_AUTH_FILE" "$d/auth.copy"; fi
# build secrets are files named in --secret id=X,src=PATH
for a in "$@"; do
  case "$a" in id=*,src=*) id=${a#id=}; id=${id%%,*}; src=${a#*,src=}; cp "$src" "$d/secret-copy-$id" ;; esac
done
[ -f "$d/stdout_$sub" ] && cat "$d/stdout_$sub"
[ -f "$d/stderr_$sub" ] && cat "$d/stderr_$sub" >&2
case "$*" in *:block*) sleep 60;; esac
if [ "$sub" = "push" ]; then
  prev=""
  for a in "$@"; do
    if [ "$prev" = "--digestfile" ]; then
      if [ -f "$d/digest" ]; then cat "$d/digest" > "$a"; else printf 'sha256:%064d' 7 > "$a"; fi
    fi
    prev="$a"
  done
fi
code=0
[ -f "$d/exit_$sub" ] && code=$(cat "$d/exit_$sub")
exit "$code"
`

// Fake is the installed fake buildah.
type Fake struct{ Dir string }

// Install puts a fake buildah first on PATH for the test.
func Install(t *testing.T) *Fake {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "buildah"), []byte(script), 0o755); err != nil {
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

// Exit sets the exit code of a subcommand ("build", "push", "rmi").
func (f *Fake) Exit(t *testing.T, sub, code string) { f.write(t, "exit_"+sub, code) }

// Stderr sets text a subcommand prints to stderr.
func (f *Fake) Stderr(t *testing.T, sub, text string) { f.write(t, "stderr_"+sub, text) }

// Digest sets the digest the fake push writes.
func (f *Fake) Digest(t *testing.T, d string) { f.write(t, "digest", d) }

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

// CallFile returns a recorded file of call n of a subcommand (extension ".env"
// or ".cwd").
func (f *Fake) CallFile(t *testing.T, sub, ext string) string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(f.Dir, "calls", "*-"+sub+ext))
	if len(files) == 0 {
		t.Fatalf("no recorded %s%s", sub, ext)
	}
	b, _ := os.ReadFile(files[0])
	return string(b)
}

// Read returns a file the fake recorded ("auth.copy", "secret-copy-<id>").
func (f *Fake) Read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.Dir, name))
	if err != nil {
		t.Fatalf("the fake recorded no %s: %v", name, err)
	}
	return string(b)
}

// Repo creates a git repository with app/Containerfile and returns its file://
// URL (which needs the plugin's allow_insecure_git setting) and the commit.
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
		"Containerfile":          "FROM scratch\nCOPY hello /hello\n",
		"hello":                  "hi\n",
		"app/Dockerfile":         "FROM scratch\n",
		"custom/Containerfile.x": "FROM scratch\n",
	} {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-q", "-m", "images")
	return "file://" + dir, run("rev-parse", "HEAD")
}
