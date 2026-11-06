package transfer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jlaffaye/ftp"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/internal/sshconn"
	"github.com/denyszorinets/damson/plugins/internal/tlsroots"
)

// statusUserNotAccepted is 430, "invalid username or password" (RFC 2228).
const statusUserNotAccepted = 430

// ConfigTLSCAPEM is the ExecRequest.Config key with extra PEM CA certificates
// trusted for ftps and for https s3 endpoints, in addition to the system roots
// (a private PKI). There is no way to skip verification.
const ConfigTLSCAPEM = tlsroots.ConfigKey

// ConfigAllowPlainFTP is the ExecRequest.Config key that permits protocol
// "ftp" (no encryption). The agent sets it from agent.allow_plain_ftp.
const ConfigAllowPlainFTP = "allow_plain_ftp"

const (
	protoFTP  = "ftp"
	protoFTPS = "ftps"

	defaultFTPPort          = 21
	defaultImplicitFTPSPort = 990
	ftpDialTimeout          = 30 * time.Second
)

// ftpFS serves a node over FTP or FTPS. Paths are absolute. The connection and
// every data connection are tracked so Close can abort a blocked transfer.
type ftpFS struct {
	c       *ftp.ServerConn
	mu      sync.Mutex
	conns   []net.Conn
	done    bool
	hadCtrl bool // the first dial is the control connection, every later one carries data
}

// ftpTarget is where and how to reach a node.
type ftpTarget struct {
	addr     string
	tls      *tls.Config // nil for plain ftp
	implicit bool
}

// ftpTargetFor derives the address and TLS settings of a node. NodeInfo.Port is
// the ssh port, so the ftp port is the node attribute ftp_port (default 21, or
// 990 for implicit ftps). ftps is explicit TLS (AUTH TLS) unless the node
// attribute ftps_mode is "implicit".
func ftpTargetFor(req pluginsdk.ExecRequest, node pluginsdk.NodeInfo, proto string) (ftpTarget, error) {
	t := ftpTarget{}
	port := defaultFTPPort
	if proto == protoFTP && req.Config[ConfigAllowPlainFTP] != "true" {
		return t, pluginsdk.ValidationError("plain ftp is not enabled on this agent (agent.allow_plain_ftp); use ftps")
	}
	if proto == protoFTPS {
		mode, _ := node.Attributes["ftps_mode"].(string)
		switch mode {
		case "", "explicit":
		case "implicit":
			t.implicit, port = true, defaultImplicitFTPSPort
		default:
			return t, pluginsdk.ValidationError("node %q: ftps_mode must be \"explicit\" or \"implicit\"", node.Name)
		}
		cfg, err := ftpsTLSConfig(req, node)
		if err != nil {
			return t, err
		}
		t.tls = cfg
	}
	if v, ok := node.Attributes["ftp_port"]; ok {
		p, err := portOf(v)
		if err != nil {
			return t, pluginsdk.ValidationError("node %q: ftp_port must be a port number", node.Name)
		}
		port = p
	}
	t.addr = net.JoinHostPort(node.Address, strconv.Itoa(port))
	return t, nil
}

func portOf(v any) (int, error) {
	f, ok := v.(float64)
	if !ok {
		if i, isInt := v.(int); isInt {
			f, ok = float64(i), true
		}
	}
	if !ok || f != float64(int(f)) || f < 1 || f > 65535 {
		return 0, errors.New("bad port")
	}
	return int(f), nil
}

// ftpsTLSConfig verifies the server certificate against the system roots plus
// the operator's extra CAs. There is no skip-verify.
func ftpsTLSConfig(req pluginsdk.ExecRequest, node pluginsdk.NodeInfo) (*tls.Config, error) {
	pool, err := tlsroots.Pool(req.Config[ConfigTLSCAPEM])
	if err != nil {
		return nil, pluginsdk.ValidationError("%s %v", ConfigTLSCAPEM, err)
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		RootCAs:            pool,
		ServerName:         node.Address,
		ClientSessionCache: tls.NewLRUClientSessionCache(8), // some servers require session reuse on data connections
	}, nil
}

// ftpCredentials resolves username and password from the node credential.
func ftpCredentials(req pluginsdk.ExecRequest, node pluginsdk.NodeInfo) (string, string, error) {
	if node.Credential == "" {
		return "", "", pluginsdk.NewError(pluginsdk.KindAuth, fmt.Sprintf("node %q has no credential", node.Name), nil)
	}
	if req.Secrets == nil {
		return "", "", pluginsdk.NewError(pluginsdk.KindAuth, "no secret resolver available", nil)
	}
	fields, err := req.Secrets.ResolveFields(node.Credential)
	if err != nil {
		return "", "", pluginsdk.NewError(pluginsdk.KindAuth, "resolving credential "+node.Credential+" failed", err)
	}
	user := fields["username"]
	if user == "" {
		user, _ = node.Attributes["username"].(string)
	}
	if user == "" || fields["password"] == "" {
		return "", "", pluginsdk.NewError(pluginsdk.KindAuth, "an ftp credential needs a username and a password", nil)
	}
	return user, fields["password"], nil
}

func dialFTP(ctx context.Context, req pluginsdk.ExecRequest, node pluginsdk.NodeInfo, proto string, emit pluginsdk.Emitter) (fileSystem, error) {
	target, err := ftpTargetFor(req, node, proto)
	if err != nil {
		return nil, err
	}
	user, pass, err := ftpCredentials(req, node)
	if err != nil {
		return nil, err
	}
	if proto == protoFTP {
		emit.Log(pluginsdk.System, "warning: ftp to "+node.Name+" sends the password and the data in clear text; use ftps")
	}
	f := &ftpFS{}
	opts := []ftp.DialOption{ftp.DialWithDialFunc(f.dialFunc(ctx, target)), ftp.DialWithShutTimeout(30 * time.Second)}
	switch {
	case target.tls != nil && target.implicit:
		// The dial function does the TLS; this makes the library ask for a
		// protected data channel (PBSZ, PROT P) after login.
		opts = append(opts, ftp.DialWithTLS(target.tls))
	case target.tls != nil:
		opts = append(opts, ftp.DialWithExplicitTLS(target.tls))
	}
	stop := context.AfterFunc(ctx, func() { _ = f.Close() })
	defer stop()
	c, err := ftp.Dial(target.addr, opts...)
	if err != nil {
		_ = f.Close()
		return nil, sshconn.CtxOr(ctx, pluginsdk.NewError(pluginsdk.KindConnection, "connecting to "+target.addr+" failed: "+err.Error(), err))
	}
	f.c = c
	if err := c.Login(user, pass); err != nil {
		_ = f.Close()
		// The TLS handshake is lazy, so a certificate problem surfaces here.
		// Only a reply from the server is an authentication failure.
		// A rejected PBSZ, PROT or FEAT (a TLS policy mismatch) is a connection
		// problem, not a bad credential.
		if !isCode(err, ftp.StatusNotLoggedIn, statusUserNotAccepted) {
			return nil, sshconn.CtxOr(ctx, pluginsdk.NewError(pluginsdk.KindConnection, "logging in to "+target.addr+" failed: "+err.Error(), err))
		}
		return nil, sshconn.CtxOr(ctx, pluginsdk.NewError(pluginsdk.KindAuth, fmt.Sprintf("logging in as %q on %s failed", user, target.addr), err))
	}
	if err := c.Type(ftp.TransferTypeBinary); err != nil {
		_ = f.Close()
		return nil, sshconn.CtxOr(ctx, pluginsdk.NewError(pluginsdk.KindConnection, "switching to binary mode failed: "+err.Error(), err))
	}
	return f, nil
}

// dialFunc dials the control connection (wrapping it in TLS for implicit ftps)
// and the data connections (wrapped in TLS for ftps), recording each so Close
// can abort them. The library calls it for the control connection first and
// for every data connection after that.
func (f *ftpFS) dialFunc(ctx context.Context, t ftpTarget) func(network, address string) (net.Conn, error) {
	return func(network, address string) (net.Conn, error) {
		d := net.Dialer{Timeout: ftpDialTimeout}
		raw, err := d.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		control, ok := f.track(raw)
		if !ok {
			_ = raw.Close()
			return nil, errors.New("connection closed")
		}
		if t.tls != nil && (t.implicit || !control) {
			// Handshake on first use, as the library does for its own data connections.
			return tls.Client(raw, t.tls), nil
		}
		return raw, nil
	}
}

// track records c and reports whether it is the control connection (the first
// one dialed) and whether the filesystem is still open. It does not compare
// addresses: a data port equal to the control port must still get TLS.
func (f *ftpFS) track(c net.Conn) (control, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		return false, false
	}
	f.conns = append(f.conns, c)
	control = !f.hadCtrl
	f.hadCtrl = true
	return control, true
}

// Close closes every connection hard, so it also aborts a blocked read or write.
func (f *ftpFS) Close() error {
	f.mu.Lock()
	conns := f.conns
	f.conns, f.done = nil, true
	f.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	return nil
}

// errTransferEnded marks a STOR that finished without an error of its own.
var errTransferEnded = errors.New("ftp transfer ended")

// isReject reports a 5xx reply from the server.
func isReject(err error) bool {
	var te *textproto.Error
	return errors.As(err, &te) && te.Code >= 500 && te.Code < 600
}

func isCode(err error, codes ...int) bool {
	var te *textproto.Error
	if !errors.As(err, &te) {
		return false
	}
	for _, c := range codes {
		if te.Code == c {
			return true
		}
	}
	return false
}

// Stat uses MLST where the server has it, else SIZE (a file) and CWD (a
// directory). 550 means the path is missing (or, for SIZE, maybe a directory).
func (f *ftpFS) Stat(_ context.Context, p string) (fileInfo, bool, error) {
	e, err := f.c.GetEntry(p)
	switch {
	case err == nil:
		return fileInfo{Size: int64(e.Size), IsDir: e.Type == ftp.EntryTypeFolder}, true, nil
	case isCode(err, ftp.StatusFileUnavailable):
		return fileInfo{}, false, nil
	case !isCode(err, ftp.StatusNotImplemented):
		return fileInfo{}, false, fmt.Errorf("stat %s: %w", p, err)
	}
	n, err := f.c.FileSize(p)
	if err == nil {
		return fileInfo{Size: n}, true, nil
	}
	if !isCode(err, ftp.StatusFileUnavailable) {
		return fileInfo{}, false, fmt.Errorf("stat %s: %w", p, err)
	}
	// 550 is "missing" or "a directory" (SIZE only works on files).
	switch err := f.c.ChangeDir(p); {
	case err == nil:
		return fileInfo{IsDir: true}, true, nil
	case isCode(err, ftp.StatusFileUnavailable):
		return fileInfo{}, false, nil
	default:
		return fileInfo{}, false, fmt.Errorf("stat %s: %w", p, err)
	}
}

func (f *ftpFS) Open(_ context.Context, p string) (io.ReadCloser, error) {
	r, err := f.c.Retr(p)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", p, err)
	}
	return r, nil
}

// Create stores a file with STOR, fed through a pipe because the library pulls
// from a reader.
func (f *ftpFS) Create(_ context.Context, p string, size int64, _ bool) (io.WriteCloser, error) {
	pr, pw := io.Pipe()
	w := &ftpWriter{pw: pw, left: size, done: make(chan error, 1)}
	go func() {
		err := f.c.Stor(p, pr)
		if err == nil {
			err = errTransferEnded
		}
		_ = pr.CloseWithError(err) // a blocked Write sees the real STOR error
		w.done <- err
	}()
	return w, nil
}

type ftpWriter struct {
	pw   *io.PipeWriter
	left int64
	done chan error
}

func (w *ftpWriter) Write(b []byte) (int, error) {
	if int64(len(b)) > w.left {
		return 0, errors.New("ftp: more data than the announced size (did the source change?)")
	}
	n, err := w.pw.Write(b)
	w.left -= int64(n)
	return n, err
}

func (w *ftpWriter) Close() error {
	_ = w.pw.Close()
	err := <-w.done
	switch {
	case err != nil && !errors.Is(err, errTransferEnded):
		return err
	case w.left != 0:
		return errShortWrite
	}
	return nil
}

func (f *ftpFS) Mkdirs(ctx context.Context, dir string) error {
	cur := ""
	for _, seg := range strings.Split(strings.Trim(path.Clean(dir), "/"), "/") {
		if seg == "" {
			continue
		}
		cur += "/" + seg
		fi, ok, err := f.Stat(ctx, cur)
		switch {
		case err != nil:
			return err
		case ok && fi.IsDir:
			continue
		case ok:
			return fmt.Errorf("mkdir %s: a file is in the way", cur)
		}
		if err := f.c.MakeDir(cur); err != nil {
			return fmt.Errorf("mkdir %s: %w", cur, err)
		}
	}
	return nil
}

// Rename: FTP has no atomic no-clobber rename, so without overwrite the target
// is checked first (best effort). With overwrite a server that refuses to
// rename onto an existing file gets the target deleted first.
func (f *ftpFS) Rename(ctx context.Context, from, to string, overwrite bool) error {
	if !overwrite {
		if _, exists, err := f.Stat(ctx, to); err != nil {
			return err
		} else if exists {
			return pluginsdk.ValidationError("destination exists: %s (set overwrite to replace it)", to)
		}
	}
	err := f.c.Rename(from, to)
	if err != nil && overwrite && isReject(err) {
		// Some servers refuse to rename onto an existing file. Delete the
		// target only when it exists, so a failure for another reason (no
		// permission, the partial is gone) never costs the old file.
		if _, exists, serr := f.Stat(ctx, to); serr == nil && exists {
			if derr := f.c.Delete(to); derr == nil {
				err = f.c.Rename(from, to)
			}
		}
	}
	if err != nil {
		return fmt.Errorf("rename to %s: %w", to, err)
	}
	return nil
}

func (f *ftpFS) Remove(_ context.Context, p string) error {
	if err := f.c.Delete(p); err != nil && !isCode(err, ftp.StatusFileUnavailable) {
		return fmt.Errorf("remove %s: %w", p, err)
	}
	return nil
}
