//go:build unix

// Package sshtest is an in-process SSH server for plugin tests. It accepts
// exec requests and runs them with the local /bin/sh, so tests exercise a real
// shell without Docker. It is test support only and is never linked into the
// damson binary.
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Options configures a Server.
type Options struct {
	// User is the only username accepted. Empty accepts any user.
	User string
	// ClientKeys are the authorized public keys.
	ClientKeys []ssh.PublicKey
	// Password, when set, enables password authentication with that password.
	Password string
	// HostKey overrides the generated host key.
	HostKey ssh.Signer
}

// Server is a running test SSH server. It stops when the test ends.
type Server struct {
	ln       net.Listener
	hostKey  ssh.Signer
	cfg      *ssh.ServerConfig
	ptys     atomic.Int32
	wg       sync.WaitGroup
	closeMu  sync.Mutex
	conns    []net.Conn
	shutdown bool
}

// NewSigner returns a fresh ed25519 signer.
func NewSigner(t testing.TB) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("creating signer: %v", err)
	}
	return s
}

// NewClientKey returns a fresh ed25519 key pair: the PEM encoded private key
// (optionally encrypted with passphrase) and the public key.
func NewClientKey(t testing.TB, passphrase string) (privatePEM string, pub ssh.PublicKey) {
	t.Helper()
	pubRaw, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	var blockBytes []byte
	if passphrase == "" {
		b, err := ssh.MarshalPrivateKey(priv, "")
		if err != nil {
			t.Fatalf("marshaling key: %v", err)
		}
		blockBytes = pemEncode(b)
	} else {
		b, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
		if err != nil {
			t.Fatalf("marshaling key: %v", err)
		}
		blockBytes = pemEncode(b)
	}
	pk, err := ssh.NewPublicKey(pubRaw)
	if err != nil {
		t.Fatalf("creating public key: %v", err)
	}
	return string(blockBytes), pk
}

// Start listens on a loopback port and serves until the test ends.
func Start(t testing.TB, opts Options) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	s := &Server{ln: ln, hostKey: opts.HostKey}
	if s.hostKey == nil {
		s.hostKey = NewSigner(t)
	}
	s.cfg = serverConfig(opts)
	s.cfg.AddHostKey(s.hostKey)
	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(s.Close)
	return s
}

func serverConfig(opts Options) *ssh.ServerConfig {
	userOK := func(u string) bool { return opts.User == "" || u == opts.User }
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if userOK(c.User()) {
				for _, k := range opts.ClientKeys {
					if string(k.Marshal()) == string(key.Marshal()) {
						return &ssh.Permissions{}, nil
					}
				}
			}
			return nil, errors.New("unauthorized key")
		},
	}
	if opts.Password != "" {
		cfg.PasswordCallback = func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if userOK(c.User()) && string(pw) == opts.Password {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("bad password")
		}
	}
	return cfg
}

// Addr returns the listen address, host:port.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Host returns the listen host.
func (s *Server) Host() string {
	h, _, _ := net.SplitHostPort(s.Addr())
	return h
}

// Port returns the listen port.
func (s *Server) Port() int {
	_, p, _ := net.SplitHostPort(s.Addr())
	n, _ := strconv.Atoi(p)
	return n
}

// HostKeyLine returns the server host key in authorized_keys format without a
// trailing newline.
func (s *Server) HostKeyLine() string { return KeyLine(s.hostKey.PublicKey()) }

// PtyRequests returns how many pty requests the server received.
func (s *Server) PtyRequests() int { return int(s.ptys.Load()) }

// KeyLine formats a public key in authorized_keys format without a newline.
func KeyLine(k ssh.PublicKey) string {
	b := ssh.MarshalAuthorizedKey(k)
	return string(b[:len(b)-1])
}

// Close stops the server and drops open connections.
func (s *Server) Close() {
	s.closeMu.Lock()
	if s.shutdown {
		s.closeMu.Unlock()
		return
	}
	s.shutdown = true
	conns := s.conns
	s.closeMu.Unlock()
	_ = s.ln.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	s.wg.Wait()
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.closeMu.Lock()
		if s.shutdown {
			s.closeMu.Unlock()
			_ = c.Close()
			return
		}
		s.conns = append(s.conns, c)
		s.wg.Add(1)
		s.closeMu.Unlock()
		go s.serve(c)
	}
}

func (s *Server) serve(c net.Conn) {
	defer s.wg.Done()
	defer c.Close()
	sc, chans, reqs, err := ssh.NewServerConn(c, s.cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	var chWG sync.WaitGroup
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only session channels")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		chWG.Add(1)
		go func() {
			defer chWG.Done()
			s.session(ch, creqs)
		}()
	}
	chWG.Wait()
}

type execMsg struct{ Command string }
type signalMsg struct{ Name string }
type exitStatusMsg struct{ Status uint32 }
type exitSignalMsg struct {
	Signal     string
	CoreDumped bool
	Message    string
	Lang       string
}

func signalByName(name string) (syscall.Signal, bool) {
	switch name {
	case "TERM":
		return syscall.SIGTERM, true
	case "INT":
		return syscall.SIGINT, true
	case "KILL":
		return syscall.SIGKILL, true
	case "HUP":
		return syscall.SIGHUP, true
	}
	return 0, false
}

func signalName(sig syscall.Signal) string {
	for _, n := range []string{"TERM", "INT", "KILL", "HUP"} {
		if s, _ := signalByName(n); s == sig {
			return n
		}
	}
	return "TERM"
}

// session serves one session channel: it collects env and pty requests, runs
// the exec request with /bin/sh and reports the exit status.
func (s *Server) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	var env []string
	var cmd *exec.Cmd
	finished := make(chan struct{})
	for req := range reqs {
		switch req.Type {
		case "env":
			var kv struct{ Name, Value string }
			if ssh.Unmarshal(req.Payload, &kv) == nil {
				env = append(env, kv.Name+"="+kv.Value)
			}
			_ = req.Reply(true, nil)
		case "pty-req":
			s.ptys.Add(1)
			_ = req.Reply(true, nil)
		case "exec":
			var m execMsg
			if cmd != nil || ssh.Unmarshal(req.Payload, &m) != nil {
				_ = req.Reply(false, nil)
				continue
			}
			cmd = exec.Command("/bin/sh", "-c", m.Command)
			cmd.Env = append(os.Environ(), env...)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Stdin, cmd.Stdout, cmd.Stderr = ch, ch, ch.Stderr()
			cmd.WaitDelay = 500 * time.Millisecond
			if err := cmd.Start(); err != nil {
				_ = req.Reply(false, nil)
				cmd = nil
				continue
			}
			_ = req.Reply(true, nil)
			go func(c *exec.Cmd) {
				defer close(finished)
				sendExit(ch, c.Wait())
				_ = ch.Close()
			}(cmd)
		case "signal":
			var m signalMsg
			if cmd != nil && ssh.Unmarshal(req.Payload, &m) == nil {
				if sig, ok := signalByName(m.Name); ok {
					_ = syscall.Kill(-cmd.Process.Pid, sig)
				}
			}
			_ = req.Reply(false, nil)
		default:
			_ = req.Reply(false, nil)
		}
	}
	// The client closed the channel: do not leave the process running.
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-finished
	}
}

func sendExit(ch ssh.Channel, err error) {
	var ee *exec.ExitError
	switch {
	case err == nil:
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(exitStatusMsg{0}))
	case errors.As(err, &ee):
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			_, _ = ch.SendRequest("exit-signal", false, ssh.Marshal(exitSignalMsg{Signal: signalName(ws.Signal())}))
			return
		}
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(exitStatusMsg{uint32(ee.ExitCode())}))
	default:
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(exitStatusMsg{255}))
	}
}
