package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/internal/sshconn"
)

// maxCommandOutput bounds the output kept from a helper command.
const maxCommandOutput = 4 << 10

// scpFS serves a node over the SCP protocol. SCP only moves file contents, so
// stat, mkdir, rename and remove run as small POSIX shell commands
// (/bin/sh -c), which every sshd with scp has. Windows targets use SFTP.
type scpFS struct {
	ssh *ssh.Client
}

func dialSCP(ctx context.Context, req pluginsdk.ExecRequest, node pluginsdk.NodeInfo, emit pluginsdk.Emitter) (fileSystem, error) {
	sc, err := dialSSH(ctx, req, node, emit)
	if err != nil {
		return nil, err
	}
	return &scpFS{ssh: sc}, nil
}

// shQuote single-quotes s for a POSIX shell.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// shell wraps a script so it runs under /bin/sh whatever the user's login shell is.
func shell(script string) string { return "/bin/sh -c " + shQuote(script) }

// command runs cmd on its own session and returns its stdout. A non-zero exit
// is an error carrying the bounded stderr. ctx ends the session.
func (s *scpFS) command(ctx context.Context, cmd string) (string, error) {
	sess, err := s.ssh.NewSession()
	if err != nil {
		return "", sshconn.CtxOr(ctx, fmt.Errorf("opening a session: %w", err))
	}
	defer func() { _ = sess.Close() }()
	var stdout, stderr limitedBuffer
	sess.Stdout, sess.Stderr = &stdout, &stderr
	stop := context.AfterFunc(ctx, func() { _ = sess.Close() })
	defer stop()
	if err := sess.Run(cmd); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return "", cerr
		}
		var ee *ssh.ExitError
		if errors.As(err, &ee) {
			return stdout.String(), &commandError{code: ee.ExitStatus(), stderr: strings.TrimSpace(stderr.String())}
		}
		return "", err
	}
	return stdout.String(), nil
}

// commandError is a remote command that exited non-zero.
type commandError struct {
	code   int
	stderr string
}

func (e *commandError) Error() string {
	if e.stderr == "" {
		return "exit status " + strconv.Itoa(e.code)
	}
	return e.stderr
}

// limitedBuffer keeps the first maxCommandOutput bytes and drops the rest.
type limitedBuffer struct{ b bytes.Buffer }

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxCommandOutput - l.b.Len(); room > 0 {
		l.b.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string { return l.b.String() }

func (s *scpFS) Stat(ctx context.Context, p string) (fileInfo, bool, error) {
	q := shQuote(p)
	out, err := s.command(ctx, shell(`if [ -d `+q+` ]; then echo dir; elif [ -e `+q+` ]; then echo file $(wc -c < `+q+`); else echo none; fi`))
	if err != nil {
		return fileInfo{}, false, fmt.Errorf("stat %s: %w", p, err)
	}
	f := strings.Fields(out)
	switch {
	case len(f) == 1 && f[0] == "none":
		return fileInfo{}, false, nil
	case len(f) == 1 && f[0] == "dir":
		return fileInfo{IsDir: true}, true, nil
	case len(f) == 2 && f[0] == "file":
		n, perr := strconv.ParseInt(f[1], 10, 64)
		if perr == nil && n >= 0 {
			return fileInfo{Size: n}, true, nil
		}
	}
	return fileInfo{}, false, fmt.Errorf("stat %s: unexpected answer %q", p, strings.TrimSpace(out))
}

func (s *scpFS) Mkdirs(ctx context.Context, dir string) error {
	if _, err := s.command(ctx, shell("mkdir -p -- "+shQuote(path.Clean(dir)))); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return nil
}

// Rename without overwrite checks for the target first; unlike SFTP there is no
// atomic no-clobber rename in POSIX, so a writer that creates the target in
// between wins (best effort, documented).
func (s *scpFS) Rename(ctx context.Context, from, to string, overwrite bool) error {
	f, t := shQuote(from), shQuote(to)
	script := "mv -f -- " + f + " " + t
	if !overwrite {
		script = "if [ -e " + t + " ]; then echo 'destination exists' >&2; exit 17; fi; mv -- " + f + " " + t
	}
	if _, err := s.command(ctx, shell(script)); err != nil {
		return fmt.Errorf("rename to %s: %w", to, err)
	}
	return nil
}

func (s *scpFS) Remove(ctx context.Context, p string) error {
	if _, err := s.command(ctx, shell("rm -f -- "+shQuote(p))); err != nil {
		return fmt.Errorf("remove %s: %w", p, err)
	}
	return nil
}

// Close is safe to call twice and while an operation is blocked.
func (s *scpFS) Close() error { return s.ssh.Close() }

var errShortWrite = errors.New("scp: wrote fewer bytes than announced")
