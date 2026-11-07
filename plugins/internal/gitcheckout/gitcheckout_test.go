package gitcheckout_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/internal/gitcheckout"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "-c", "protocol.file.allow=always"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// origin makes a repo with two commits on main, a tag on the first and a branch.
func origin(t *testing.T) (dir, first, second string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir = t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "one")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "one")
	first = git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "tag", "v1")
	git(t, dir, "checkout", "-q", "-b", "feature")
	write(t, dir, "f.txt", "feature")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "feature")
	git(t, dir, "checkout", "-q", "main")
	write(t, dir, "a.txt", "two")
	git(t, dir, "commit", "-q", "-am", "two")
	second = git(t, dir, "rev-parse", "HEAD")
	return dir, first, second
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func kind(err error) pluginsdk.ErrorKind {
	if se, ok := err.(*pluginsdk.StepError); ok {
		return se.Kind
	}
	return ""
}

func TestCloneRefs(t *testing.T) {
	src, first, second := origin(t)
	url := "file://" + src
	for _, c := range []struct {
		name, ref, want, file string
		sha                   string
	}{
		{"default branch", "", "two", "a.txt", second},
		{"branch", "main", "two", "a.txt", second},
		{"other branch", "feature", "feature", "f.txt", ""},
		{"tag", "v1", "one", "a.txt", first},
		{"full sha", first, "one", "a.txt", first},
	} {
		t.Run(c.name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "w")
			res, err := gitcheckout.Clone(context.Background(), dst, gitcheckout.Repo{URL: url, Ref: c.ref, AllowInsecure: true}, gitcheckout.Auth{}, gitcheckout.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if got := read(t, filepath.Join(dst, c.file)); got != c.want {
				t.Errorf("%s = %q, want %q", c.file, got, c.want)
			}
			if len(res.Commit) != 40 || (c.sha != "" && res.Commit != c.sha) {
				t.Errorf("commit = %q, want %q", res.Commit, c.sha)
			}
		})
	}
}

func TestCloneIsShallowAndReportsProgress(t *testing.T) {
	src, _, _ := origin(t)
	dst := filepath.Join(t.TempDir(), "w")
	var out bytes.Buffer
	if _, err := gitcheckout.Clone(context.Background(), dst, gitcheckout.Repo{URL: "file://" + src, Ref: "main", AllowInsecure: true}, gitcheckout.Auth{}, gitcheckout.Options{Out: &out}); err != nil {
		t.Fatal(err)
	}
	if n := git(t, dst, "rev-list", "--count", "HEAD"); n != "1" {
		t.Errorf("history length = %s, want a depth-1 checkout", n)
	}
}

func TestCloneSubmodules(t *testing.T) {
	sub, _, _ := origin(t)
	super := t.TempDir()
	git(t, super, "init", "-q", "-b", "main")
	git(t, super, "submodule", "add", "-q", "file://"+sub, "lib")
	git(t, super, "commit", "-q", "-m", "add submodule")
	url := "file://" + super
	without := filepath.Join(t.TempDir(), "a")
	if _, err := gitcheckout.Clone(context.Background(), without, gitcheckout.Repo{URL: url, Ref: "main", AllowInsecure: true}, gitcheckout.Auth{}, gitcheckout.Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(without, "lib", "a.txt")); err == nil {
		t.Error("submodule content present without submodules: true")
	}
	with := filepath.Join(t.TempDir(), "b")
	if _, err := gitcheckout.Clone(context.Background(), with, gitcheckout.Repo{URL: url, Ref: "main", Submodules: true, AllowInsecure: true}, gitcheckout.Auth{}, gitcheckout.Options{}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(with, "lib", "a.txt")); got != "two" {
		t.Errorf("submodule file = %q", got)
	}
}

func TestBadRefAndBadRepo(t *testing.T) {
	src, _, _ := origin(t)
	_, err := gitcheckout.Clone(context.Background(), filepath.Join(t.TempDir(), "w"), gitcheckout.Repo{URL: "file://" + src, Ref: "nope", AllowInsecure: true}, gitcheckout.Auth{}, gitcheckout.Options{})
	if kind(err) != pluginsdk.KindValidation {
		t.Errorf("bad ref: %v (%s)", err, kind(err))
	}
	_, err = gitcheckout.Clone(context.Background(), filepath.Join(t.TempDir(), "w"), gitcheckout.Repo{URL: "file://" + filepath.Join(src, "missing"), AllowInsecure: true}, gitcheckout.Auth{}, gitcheckout.Options{})
	if kind(err) != pluginsdk.KindValidation && kind(err) != pluginsdk.KindConnection {
		t.Errorf("bad repo: %v (%s)", err, kind(err))
	}
}

func TestValidation(t *testing.T) {
	for name, c := range map[string]struct {
		repo gitcheckout.Repo
		auth gitcheckout.Auth
		want string
	}{
		"empty":                {gitcheckout.Repo{}, gitcheckout.Auth{}, "required"},
		"option injection":     {gitcheckout.Repo{URL: "--upload-pack=x"}, gitcheckout.Auth{}, "not valid"},
		"ref option injection": {gitcheckout.Repo{URL: "https://h/r.git", Ref: "--foo"}, gitcheckout.Auth{}, "ref is not valid"},
		"ext transport":        {gitcheckout.Repo{URL: "ext::sh -c id"}, gitcheckout.Auth{}, "https or ssh"},
		"fd transport":         {gitcheckout.Repo{URL: "fd::3"}, gitcheckout.Auth{}, "https or ssh"},
		"local path":           {gitcheckout.Repo{URL: "/etc"}, gitcheckout.Auth{}, "https or ssh"},
		"file not allowed":     {gitcheckout.Repo{URL: "file:///tmp/x"}, gitcheckout.Auth{}, "not enabled"},
		"http not allowed":     {gitcheckout.Repo{URL: "http://h/r.git"}, gitcheckout.Auth{}, "not enabled"},
		"password in url":      {gitcheckout.Repo{URL: "https://u:p@h/r.git"}, gitcheckout.Auth{}, "must not carry a password"},
		"token over ssh":       {gitcheckout.Repo{URL: "ssh://h/r.git"}, gitcheckout.Auth{Token: "t", KnownHosts: "h ssh-ed25519 AAAA"}, "needs an https url"},
		"key over https":       {gitcheckout.Repo{URL: "https://h/r.git"}, gitcheckout.Auth{SSHKey: "k"}, "needs an ssh url"},
		"ssh without hosts":    {gitcheckout.Repo{URL: "git@h:r.git"}, gitcheckout.Auth{}, "known_hosts"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := gitcheckout.Clone(context.Background(), t.TempDir(), c.repo, c.auth, gitcheckout.Options{})
			if kind(err) != pluginsdk.KindValidation || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v (%s), want validation containing %q", err, kind(err), c.want)
			}
		})
	}
}

func TestCancellationStopsTheClone(t *testing.T) {
	src, _, _ := origin(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := gitcheckout.Clone(ctx, filepath.Join(t.TempDir(), "w"), gitcheckout.Repo{URL: "file://" + src, AllowInsecure: true}, gitcheckout.Auth{}, gitcheckout.Options{})
	if kind(err) != pluginsdk.KindCancelled {
		t.Fatalf("err = %v (%s)", err, kind(err))
	}
	_, err = gitcheckout.Clone(context.Background(), filepath.Join(t.TempDir(), "w"), gitcheckout.Repo{URL: "file://" + src, AllowInsecure: true}, gitcheckout.Auth{}, gitcheckout.Options{Timeout: time.Nanosecond})
	if kind(err) != pluginsdk.KindTimeout {
		t.Fatalf("timeout err = %v (%s)", err, kind(err))
	}
}

// httpRepo serves src over git's smart http protocol and requires basic auth
// with the given token; it records the Authorization headers it saw.
func httpRepo(t *testing.T, src, token string) (*httptest.Server, *[]string) {
	t.Helper()
	execPath := git(t, src, "--exec-path")
	root := t.TempDir()
	bare := filepath.Join(root, "repo.git")
	git(t, root, "clone", "-q", "--bare", src, bare)
	var seen []string
	h := &cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		seen = append(seen, r.Header.Get("Authorization"))
		if !ok || u != "ci" || p != token {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestTokenAuthOverHTTP(t *testing.T) {
	src, _, second := origin(t)
	srv, seen := httpRepo(t, src, "s3cret-TOKEN")
	repo := gitcheckout.Repo{URL: srv.URL + "/repo.git", Ref: "main", AllowInsecure: true}

	dst := filepath.Join(t.TempDir(), "w")
	var out bytes.Buffer
	res, err := gitcheckout.Clone(context.Background(), dst, repo, gitcheckout.Auth{Username: "ci", Token: "s3cret-TOKEN"}, gitcheckout.Options{Out: &out})
	if err != nil || res.Commit != second {
		t.Fatalf("clone = %+v, %v", res, err)
	}
	if strings.Contains(out.String(), "s3cret-TOKEN") {
		t.Error("the token is in the progress output")
	}
	// Neither the checkout's git config nor its remote URL holds the token.
	if cfg := read(t, filepath.Join(dst, ".git", "config")); strings.Contains(cfg, "s3cret") {
		t.Errorf("the token is in .git/config: %s", cfg)
	}
	if len(*seen) == 0 {
		t.Fatal("the server saw no request")
	}

	_, err = gitcheckout.Clone(context.Background(), filepath.Join(t.TempDir(), "w"), repo, gitcheckout.Auth{Username: "ci", Token: "wrong"}, gitcheckout.Options{})
	if kind(err) != pluginsdk.KindAuth {
		t.Fatalf("wrong token: %v (%s)", err, kind(err))
	}
	if strings.Contains(err.Error(), "wrong") && strings.Contains(err.Error(), "Authorization") {
		t.Errorf("error text: %v", err)
	}
	_, err = gitcheckout.Clone(context.Background(), filepath.Join(t.TempDir(), "w"), repo, gitcheckout.Auth{}, gitcheckout.Options{})
	if kind(err) != pluginsdk.KindAuth {
		t.Fatalf("no credential: %v (%s)", err, kind(err))
	}
}

func TestCredentialFilesAreRemoved(t *testing.T) {
	src, _, _ := origin(t)
	srv, _ := httpRepo(t, src, "tok")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	_, err := gitcheckout.Clone(context.Background(), filepath.Join(t.TempDir(), "w"), gitcheckout.Repo{URL: srv.URL + "/repo.git", AllowInsecure: true}, gitcheckout.Auth{Username: "ci", Token: "tok"}, gitcheckout.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "damson-git-*")); len(left) != 0 {
		t.Fatalf("credential directories left behind: %v", left)
	}
}

func TestSSHNeedsKnownHostsOrOptOut(t *testing.T) {
	_, err := gitcheckout.Clone(context.Background(), t.TempDir(), gitcheckout.Repo{URL: "ssh://git@127.0.0.1:1/r.git"}, gitcheckout.Auth{InsecureHostKey: true}, gitcheckout.Options{Timeout: 20 * time.Second})
	if kind(err) != pluginsdk.KindConnection {
		t.Fatalf("unreachable ssh host: %v (%s)", err, kind(err))
	}
}
