package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/internal/sshconn"
)

// sftpFS serves a node over SFTP on its own SSH connection.
type sftpFS struct {
	ssh    *ssh.Client
	client *sftp.Client
	once   sync.Once
}

// dialSSH connects to the node with the shared SSH client code (host key
// policy, TOFU, credentials). A trust-on-first-use key is reported once
// authentication succeeded.
func dialSSH(ctx context.Context, req pluginsdk.ExecRequest, node pluginsdk.NodeInfo, emit pluginsdk.Emitter) (*ssh.Client, error) {
	if node.Transport != "" && node.Transport != "ssh" {
		return nil, pluginsdk.ValidationError("node %q uses transport %q; this protocol needs an ssh node", node.Name, node.Transport)
	}
	sc, hk, err := sshconn.Dial(ctx, req, node, "")
	if err != nil {
		return nil, err
	}
	hk.Report(req, node, emit)
	return sc, nil
}

// dialSFTP opens the sftp subsystem on a node.
func dialSFTP(ctx context.Context, req pluginsdk.ExecRequest, node pluginsdk.NodeInfo, emit pluginsdk.Emitter) (fileSystem, error) {
	sc, err := dialSSH(ctx, req, node, emit)
	if err != nil {
		return nil, err
	}
	c, err := sftp.NewClient(sc)
	if err != nil {
		_ = sc.Close()
		return nil, sshconn.CtxOr(ctx, pluginsdk.NewError(pluginsdk.KindConnection, "opening the sftp subsystem on "+node.Name+" failed: "+err.Error(), err))
	}
	return &sftpFS{ssh: sc, client: c}, nil
}

func (s *sftpFS) Stat(_ context.Context, p string) (fileInfo, bool, error) {
	fi, err := s.client.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fileInfo{}, false, nil
	case err != nil:
		return fileInfo{}, false, fmt.Errorf("stat %s: %w", p, err)
	}
	return fileInfo{Size: fi.Size(), IsDir: fi.IsDir()}, true, nil
}

func (s *sftpFS) Open(_ context.Context, p string) (io.ReadCloser, error) {
	f, err := s.client.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", p, err)
	}
	return f, nil
}

func (s *sftpFS) Create(_ context.Context, p string, _ int64) (io.WriteCloser, error) {
	// O_EXCL: the name is unique per attempt, so a clash means something is wrong.
	f, err := s.client.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", p, err)
	}
	return f, nil
}

func (s *sftpFS) Mkdirs(_ context.Context, dir string) error {
	if err := s.client.MkdirAll(path.Clean(dir)); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return nil
}

// Rename uses plain SSH_FXP_RENAME, which fails when the target exists, to
// close the race behind overwrite=false; overwrite uses posix-rename.
func (s *sftpFS) Rename(_ context.Context, from, to string, overwrite bool) error {
	var err error
	if overwrite {
		err = s.client.PosixRename(from, to)
	} else {
		err = s.client.Rename(from, to)
	}
	if err != nil {
		return fmt.Errorf("rename to %s: %w", to, err)
	}
	return nil
}

func (s *sftpFS) Remove(_ context.Context, p string) error {
	if err := s.client.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", p, err)
	}
	return nil
}

// Close is safe to call twice and while an operation is blocked: the SSH
// connection goes first, because closing the sftp client alone waits for the
// server to answer.
func (s *sftpFS) Close() error {
	s.once.Do(func() {
		_ = s.ssh.Close()
		_ = s.client.Close()
	})
	return nil
}
