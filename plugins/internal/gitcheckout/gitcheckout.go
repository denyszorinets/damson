// Package gitcheckout is the shared git clone helper of the ansible, buildah
// and helm plugins (ARCHITECTURE 3 and 6.4 to 6.6). It shells out to the git
// binary (capability bin:git) and nothing else.
//
// Credentials never appear in an argument list, a URL or a log line: a token is
// handed to git through a GIT_ASKPASS script that reads it from a 0600 file, an
// SSH key is a 0600 file named by GIT_SSH_COMMAND. The files live in a private
// directory that Clone removes before it returns. The transport allow-list is
// set explicitly, so an "ext::" or "fd::" URL can never run a command.
package gitcheckout

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Repo names what to check out.
type Repo struct {
	URL        string // https://, ssh:// or scp-like git@host:path
	Ref        string // branch, tag or full 40-hex commit; empty is the default branch
	Submodules bool
	// AllowInsecure also permits http://, git:// and file:// URLs. The plugins
	// set it only from an operator setting, and the tests use it for local repos.
	AllowInsecure bool
}

// Auth is the credential for a repo; the zero value is anonymous access.
type Auth struct {
	Username string // https basic user; defaults to "git"
	Token    string // https password or token
	SSHKey   string // PEM private key
	// KnownHosts is the content of a known_hosts file used to verify the ssh
	// host. Without it an ssh clone fails unless InsecureHostKey is set.
	KnownHosts      string
	InsecureHostKey bool
}

// Options tunes Clone.
type Options struct {
	// Out receives git's progress output (stderr), line by line as it arrives.
	Out io.Writer
	// Timeout bounds the whole clone; zero means no limit besides ctx.
	Timeout time.Duration
	// Git is the git binary; empty means "git" from PATH.
	Git string
}

// Result is what Clone resolved.
type Result struct {
	Commit string // full SHA of HEAD in the checkout
}

// Clone fetches repo at depth 1 into dir (which must be empty or absent) and
// checks it out detached. It returns the resolved commit.
func Clone(ctx context.Context, dir string, repo Repo, auth Auth, opt Options) (Result, error) {
	if err := validate(repo, auth); err != nil {
		return Result{}, err
	}
	if opt.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opt.Timeout)
		defer cancel()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, pluginsdk.NewError(pluginsdk.KindInternal, "creating the checkout directory failed", err)
	}
	secretDir, err := os.MkdirTemp("", "damson-git-*")
	if err != nil {
		return Result{}, pluginsdk.NewError(pluginsdk.KindInternal, "creating the credentials directory failed", err)
	}
	defer func() { _ = os.RemoveAll(secretDir) }()
	env, err := environment(secretDir, repo, auth)
	if err != nil {
		return Result{}, err
	}
	r := runner{git: opt.Git, dir: dir, env: env, out: opt.Out}
	if r.git == "" {
		r.git = "git"
	}
	remote := repo.URL
	steps := [][]string{
		{"init", "--quiet", "."},
		{"remote", "add", "origin", "--", remote},
	}
	for _, args := range steps {
		if _, err := r.run(ctx, args...); err != nil {
			return Result{}, err
		}
	}
	ref := repo.Ref
	if ref == "" {
		ref = "HEAD"
	}
	if _, err := r.run(ctx, "fetch", "--quiet", "--depth", "1", "--no-tags", "origin", ref); err != nil {
		return Result{}, err
	}
	if _, err := r.run(ctx, "-c", "advice.detachedHead=false", "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		return Result{}, err
	}
	if repo.Submodules {
		if _, err := r.run(ctx, "submodule", "update", "--init", "--recursive", "--depth", "1", "--quiet"); err != nil {
			return Result{}, err
		}
	}
	sha, err := r.run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return Result{}, err
	}
	return Result{Commit: strings.TrimSpace(sha)}, nil
}

func validate(repo Repo, auth Auth) error {
	if repo.URL == "" {
		return pluginsdk.ValidationError("the repository url is required")
	}
	if strings.HasPrefix(repo.URL, "-") || strings.ContainsAny(repo.URL, "\x00\n\r") {
		return pluginsdk.ValidationError("the repository url is not valid")
	}
	if strings.HasPrefix(repo.Ref, "-") || strings.ContainsAny(repo.Ref, "\x00\n\r ") {
		return pluginsdk.ValidationError("the ref is not valid")
	}
	scheme, err := schemeOf(repo.URL)
	if err != nil {
		return err
	}
	switch scheme {
	case "https", "ssh":
	case "http", "git", "file":
		if !repo.AllowInsecure {
			return pluginsdk.ValidationError("the %s transport is not enabled; use https or ssh", scheme)
		}
	default:
		return pluginsdk.ValidationError("the repository url must use https or ssh")
	}
	if u, perr := url.Parse(repo.URL); perr == nil && u.User != nil {
		if _, has := u.User.Password(); has {
			return pluginsdk.ValidationError("the repository url must not carry a password; use a credential")
		}
	}
	if auth.Token != "" && scheme != "https" && scheme != "http" {
		return pluginsdk.ValidationError("a token credential needs an https url")
	}
	if auth.SSHKey != "" && scheme != "ssh" {
		return pluginsdk.ValidationError("an ssh key credential needs an ssh url")
	}
	if scheme == "ssh" && auth.KnownHosts == "" && !auth.InsecureHostKey {
		return pluginsdk.ValidationError("an ssh repository needs known_hosts to verify the host (or the operator must allow insecure host keys)")
	}
	return nil
}

// schemeOf returns the transport of a url, treating scp-like host:path as ssh.
func schemeOf(raw string) (string, error) {
	if i := strings.Index(raw, "://"); i > 0 {
		return strings.ToLower(raw[:i]), nil
	}
	if strings.Contains(raw, "::") { // ext::, fd:: and friends
		return "", pluginsdk.ValidationError("the repository url must use https or ssh")
	}
	if strings.Contains(raw, ":") && !strings.HasPrefix(raw, "/") {
		return "ssh", nil
	}
	return "", pluginsdk.ValidationError("the repository url must use https or ssh")
}

type runner struct {
	git, dir string
	env      []string
	out      io.Writer
}

// run executes one git command and returns its stdout. stderr goes to out and
// is kept (bounded) for the error text.
func (r runner) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.git, args...)
	cmd.Dir = r.dir
	cmd.Env = r.env
	prepare(cmd)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	errBuf := &tailBuffer{max: 4096}
	if r.out != nil {
		cmd.Stderr = io.MultiWriter(errBuf, r.out)
	} else {
		cmd.Stderr = errBuf
	}
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			kind := pluginsdk.KindCancelled
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				kind = pluginsdk.KindTimeout
			}
			return "", pluginsdk.NewError(kind, "git "+args[0]+" was interrupted", ctx.Err())
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", classify(args[0], errBuf.String(), err)
		}
		return "", pluginsdk.NewError(pluginsdk.KindInternal, "running git failed", err)
	}
	return stdout.String(), nil
}

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return strings.TrimSpace(string(t.b)) }

func credPath(dir, name string) string { return filepath.Join(dir, name) }

var errNoGit = fmt.Errorf("git not found")
