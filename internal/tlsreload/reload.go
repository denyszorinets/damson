// Package tlsreload serves a certificate from files and picks up a renewed one
// without a restart (cert-manager, certbot or an operator replacing the files).
package tlsreload

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// CheckInterval is how often the files are looked at for a change, at most
// once per handshake that falls after it.
const CheckInterval = 10 * time.Second

type stamp struct {
	certMod, keyMod   time.Time
	certSize, keySize int64
}

// Reloader hands out the current certificate.
type Reloader struct {
	certFile, keyFile string
	log               *slog.Logger
	now               func() time.Time

	mu      sync.Mutex
	cert    *tls.Certificate
	seen    stamp
	checked time.Time
}

// New loads the pair once; an unreadable or mismatched pair is an error, so a
// bad configuration stops the controller at start.
func New(certFile, keyFile string, log *slog.Logger) (*Reloader, error) {
	r := &Reloader{certFile: certFile, keyFile: keyFile, log: log, now: time.Now}
	if err := r.load(); err != nil {
		return nil, err
	}
	r.checked = r.now()
	return r, nil
}

func (r *Reloader) stat() (stamp, error) {
	c, err := os.Stat(r.certFile)
	if err != nil {
		return stamp{}, err
	}
	k, err := os.Stat(r.keyFile)
	if err != nil {
		return stamp{}, err
	}
	return stamp{c.ModTime(), k.ModTime(), c.Size(), k.Size()}, nil
}

func (r *Reloader) load() error {
	st, err := r.stat()
	if err != nil {
		return fmt.Errorf("reading the controller tls files: %w", err)
	}
	pair, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("loading the controller tls certificate %s and key %s: %w", r.certFile, r.keyFile, err)
	}
	r.cert, r.seen = &pair, st
	return nil
}

// GetCertificate is tls.Config.GetCertificate. A failed reload keeps serving
// the old certificate and logs the error (once per change).
func (r *Reloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now := r.now(); now.Sub(r.checked) >= CheckInterval {
		r.checked = now
		if st, err := r.stat(); err == nil && st != r.seen {
			if err := r.load(); err != nil {
				r.seen = st // do not retry, or log, until the files change again
				r.log.Error("controller tls certificate not reloaded; still serving the previous one", "error", err)
			} else {
				r.log.Info("controller tls certificate reloaded")
			}
		}
	}
	if r.cert == nil {
		return nil, errors.New("no certificate")
	}
	return r.cert, nil
}

// Config is a server configuration: TLS 1.2 or newer, HTTP/1.1 only (the agent
// sockets are WebSockets).
func (r *Reloader) Config() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: r.GetCertificate, NextProtos: []string{"http/1.1"}}
}
