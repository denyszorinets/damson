// Package s3test is an in-process S3-compatible server for plugin tests. It is
// a test helper: plugins never import it from non-test code.
package s3test

import (
	"encoding/pem"
	"net/http/httptest"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

// Credentials the tests use; the fake server does not check them.
const (
	AccessKey = "AKIATESTACCESSKEY0000"
	SecretKey = "test-secret-key-DO-NOT-LEAK-0123456789"
)

// Server is a running fake S3; it stops when the test ends.
type Server struct {
	ts *httptest.Server
	// CAPEM is the PEM certificate of an https server (StartTLS), for the
	// tls_ca_pem plugin setting. Empty for an http server.
	CAPEM string
}

// Start runs an http server with the given buckets.
func Start(t testing.TB, buckets ...string) *Server { return start(t, false, buckets) }

// StartTLS runs an https server whose certificate is signed by no known CA;
// CAPEM is that certificate.
func StartTLS(t testing.TB, buckets ...string) *Server { return start(t, true, buckets) }

func start(t testing.TB, tlsOn bool, buckets []string) *Server {
	t.Helper()
	backend := s3mem.New()
	for _, b := range buckets {
		if err := backend.CreateBucket(b); err != nil {
			t.Fatal(err)
		}
	}
	h := gofakes3.New(backend).Server()
	if !tlsOn {
		ts := httptest.NewServer(h)
		t.Cleanup(ts.Close)
		return &Server{ts: ts}
	}
	ts := httptest.NewTLSServer(h)
	t.Cleanup(ts.Close)
	pemCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw})
	return &Server{ts: ts, CAPEM: string(pemCert)}
}

// URL is the http endpoint of the server.
func (s *Server) URL() string { return s.ts.URL }

// Connection returns the fields of a connection secret for the server.
func (s *Server) Connection() map[string]string {
	return map[string]string{
		"endpoint": s.URL(), "region": "us-east-1", "access_key": AccessKey, "secret_key": SecretKey, "path_style": "true",
	}
}
