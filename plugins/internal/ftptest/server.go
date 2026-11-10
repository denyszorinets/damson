//go:build unix

// Package ftptest is an in-process FTP and FTPS server on the real file
// system, for plugin tests. It is a test helper: plugins never import it from
// non-test code.
package ftptest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/spf13/afero"
)

// Mode selects the transport security of the server.
type Mode int

// Modes.
const (
	Plain    Mode = iota // plain FTP
	Explicit             // FTPS, AUTH TLS required
	Implicit             // FTPS, TLS from the first byte
)

// Options configures a Server.
type Options struct {
	User, Password string
	Mode           Mode
	// DNSOnly gives the certificate the name "localhost" and no IP address,
	// to test verification of a hostname node.
	DNSOnly bool
}

// Server is a running test FTP server; it stops when the test ends.
type Server struct {
	srv      *ftpserver.FtpServer
	host     string
	port     int
	attempts atomic.Int32
	// CAPEM is the PEM certificate that signed the server certificate, for
	// the tls_ca_pem plugin setting. Empty for Plain.
	CAPEM string
}

// Host and Port are where the server listens.
func (s *Server) Host() string { return s.host }

// Port is the TCP port.
func (s *Server) Port() int { return s.port }

type driver struct {
	s    *Server
	opts Options
	ln   net.Listener
	tls  *tls.Config
}

func (d *driver) GetSettings() (*ftpserver.Settings, error) {
	st := &ftpserver.Settings{Listener: d.ln, DisableActiveMode: true}
	if d.opts.Mode == Explicit {
		st.TLSRequired = ftpserver.MandatoryEncryption
	}
	return st, nil
}

func (*driver) ClientConnected(ftpserver.ClientContext) (string, error) {
	return "damson test ftp", nil
}
func (*driver) ClientDisconnected(ftpserver.ClientContext) {}

func (d *driver) AuthUser(_ ftpserver.ClientContext, user, pass string) (ftpserver.ClientDriver, error) {
	d.s.attempts.Add(1)
	if user != d.opts.User || pass != d.opts.Password {
		return nil, errors.New("bad credentials")
	}
	return afero.NewOsFs(), nil
}

func (d *driver) GetTLSConfig() (*tls.Config, error) {
	if d.tls == nil {
		return nil, errors.New("tls is not enabled")
	}
	return d.tls, nil
}

// Start runs a server on 127.0.0.1 with a random port.
func Start(t testing.TB, opts Options) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{host: "127.0.0.1"}
	d := &driver{s: s, opts: opts}
	if opts.Mode != Plain {
		cfg, caPEM := tlsConfig(t, opts.DNSOnly)
		d.tls, s.CAPEM = cfg, caPEM
		if opts.Mode == Implicit {
			ln = tls.NewListener(ln, cfg)
		}
	}
	d.ln = ln
	s.port = ln.Addr().(*net.TCPAddr).Port
	s.srv = ftpserver.NewFtpServer(d)
	if err := s.srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.srv.Serve() }()
	t.Cleanup(func() { _ = s.srv.Stop() })
	return s
}

// AuthAttempts is how many USER/PASS logins reached the server, to show that
// a refused TLS upgrade stopped the client before it sent credentials.
func (s *Server) AuthAttempts() int { return int(s.attempts.Load()) }

// Addr is host:port.
func (s *Server) Addr() string { return net.JoinHostPort(s.host, strconv.Itoa(s.port)) }

// tlsConfig makes a CA and a server certificate for 127.0.0.1 signed by it.
func tlsConfig(t testing.TB, dnsOnly bool) (*tls.Config, string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "damson test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if dnsOnly {
		tmpl.IPAddresses, tmpl.DNSNames, tmpl.Subject.CommonName = nil, []string{"localhost"}, "localhost"
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		},
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
}
