package transfer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/denyszorinets/damson/plugins/internal/sshconn"
)

// The SCP wire protocol, the part this plugin needs (one regular file, no
// directories, no timestamps). After every message the receiver answers with a
// status byte: 0 is ok, 1 a warning and 2 a fatal error, both followed by a
// text line.
//
//	source (scp -f):  C<mode> <size> <name>\n  <size bytes>  \0
//	sink   (scp -t):  the same, sent by us
const scpFileMode = "0644"

// scpSession is a started scp process with its pipes.
type scpSession struct {
	sess *ssh.Session
	in   io.WriteCloser
	out  *bufio.Reader
	stop func() bool
}

// startSCP runs "scp <flag> -- <target>" on its own session.
func (s *scpFS) startSCP(ctx context.Context, flag, target string) (*scpSession, error) {
	sess, err := s.ssh.NewSession()
	if err != nil {
		return nil, sshconn.CtxOr(ctx, fmt.Errorf("opening a session: %w", err))
	}
	in, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	out, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	if err := sess.Start("scp " + flag + " -- " + shQuote(target)); err != nil {
		_ = sess.Close()
		return nil, sshconn.CtxOr(ctx, fmt.Errorf("starting scp: %w", err))
	}
	return &scpSession{sess: sess, in: in, out: bufio.NewReader(out), stop: context.AfterFunc(ctx, func() { _ = sess.Close() })}, nil
}

func (c *scpSession) close() {
	c.stop()
	_ = c.in.Close()
	_ = c.sess.Close()
}

// ack reads a status byte and turns a warning or fatal error into an error.
func (c *scpSession) ack() error {
	b, err := c.out.ReadByte()
	if err != nil {
		return fmt.Errorf("scp: reading the status: %w", err)
	}
	if b == 0 {
		return nil
	}
	msg, _ := c.out.ReadString('\n')
	return fmt.Errorf("scp: %s", strings.TrimSpace(msg))
}

func (c *scpSession) sendOK() error {
	_, err := c.in.Write([]byte{0})
	return err
}

// Open reads a file with "scp -f".
func (s *scpFS) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	c, err := s.startSCP(ctx, "-f", p)
	if err != nil {
		return nil, err
	}
	if err := c.sendOK(); err != nil {
		c.close()
		return nil, sshconn.CtxOr(ctx, err)
	}
	line, err := c.out.ReadString('\n')
	if err != nil {
		c.close()
		return nil, sshconn.CtxOr(ctx, fmt.Errorf("scp: reading the file header: %w", err))
	}
	size, err := parseHeader(line)
	if err != nil {
		c.close()
		return nil, err
	}
	if err := c.sendOK(); err != nil {
		c.close()
		return nil, sshconn.CtxOr(ctx, err)
	}
	return &scpReader{c: c, left: size}, nil
}

// parseHeader reads "C<mode> <size> <name>" and returns the size. A status
// byte in front (1 or 2) is an error message from the remote scp.
func parseHeader(line string) (int64, error) {
	if line != "" && (line[0] == 1 || line[0] == 2) {
		return 0, fmt.Errorf("scp: %s", strings.TrimSpace(line[1:]))
	}
	f := strings.SplitN(strings.TrimSuffix(line, "\n"), " ", 3)
	if len(f) != 3 || !strings.HasPrefix(f[0], "C") {
		return 0, fmt.Errorf("scp: unexpected header %q (is the source a regular file?)", strings.TrimSpace(line))
	}
	size, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil || size < 0 {
		return 0, fmt.Errorf("scp: invalid size in header %q", strings.TrimSpace(line))
	}
	return size, nil
}

// scpReader yields the announced number of bytes, then consumes the trailing
// status byte and acknowledges it.
type scpReader struct {
	c    *scpSession
	left int64
	done bool
}

func (r *scpReader) Read(b []byte) (int, error) {
	if r.left == 0 {
		return 0, r.finish()
	}
	if int64(len(b)) > r.left {
		b = b[:r.left]
	}
	n, err := r.c.out.Read(b)
	r.left -= int64(n)
	if err == io.EOF && r.left > 0 {
		err = io.ErrUnexpectedEOF
	}
	if err == nil && r.left == 0 {
		return n, nil
	}
	return n, err
}

func (r *scpReader) finish() error {
	if r.done {
		return io.EOF
	}
	r.done = true
	if err := r.c.ack(); err != nil {
		return err
	}
	_ = r.c.sendOK()
	return io.EOF
}

func (r *scpReader) Close() error { r.c.close(); return nil }

// Create writes a file with "scp -t". The target is the full file path, so
// scp uses the header name only as a label.
func (s *scpFS) Create(ctx context.Context, p string, size int64) (io.WriteCloser, error) {
	c, err := s.startSCP(ctx, "-t", p)
	if err != nil {
		return nil, err
	}
	if err := c.ack(); err != nil {
		c.close()
		return nil, sshconn.CtxOr(ctx, err)
	}
	if _, err := fmt.Fprintf(c.in, "C%s %d %s\n", scpFileMode, size, path.Base(p)); err != nil {
		c.close()
		return nil, sshconn.CtxOr(ctx, err)
	}
	if err := c.ack(); err != nil {
		c.close()
		return nil, sshconn.CtxOr(ctx, err)
	}
	return &scpWriter{c: c, left: size}, nil
}

type scpWriter struct {
	c    *scpSession
	left int64
}

func (w *scpWriter) Write(b []byte) (int, error) {
	if int64(len(b)) > w.left {
		return 0, errors.New("scp: more data than the announced size (did the source change?)")
	}
	n, err := w.c.in.Write(b)
	w.left -= int64(n)
	return n, err
}

// Close ends the file: the data must be complete, then the trailing zero byte
// is sent and the remote status read.
func (w *scpWriter) Close() error {
	defer w.c.close()
	if w.left != 0 {
		return errShortWrite
	}
	if err := w.c.sendOK(); err != nil {
		return err
	}
	if err := w.c.ack(); err != nil {
		return err
	}
	_ = w.c.in.Close()
	if err := w.c.sess.Wait(); err != nil {
		var ee *ssh.ExitError
		if !errors.As(err, &ee) {
			return err
		}
		return fmt.Errorf("scp exited with status %d", ee.ExitStatus())
	}
	return nil
}
