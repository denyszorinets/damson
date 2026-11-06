// Package s3test is an in-process S3-compatible server for plugin tests. It is
// a test helper: plugins never import it from non-test code.
package s3test

import (
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
}

// Start runs a server with the given buckets.
func Start(t testing.TB, buckets ...string) *Server {
	t.Helper()
	backend := s3mem.New()
	for _, b := range buckets {
		if err := backend.CreateBucket(b); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(ts.Close)
	return &Server{ts: ts}
}

// URL is the http endpoint of the server.
func (s *Server) URL() string { return s.ts.URL }

// Connection returns the fields of a connection secret for the server.
func (s *Server) Connection() map[string]string {
	return map[string]string{
		"endpoint": s.URL(), "region": "us-east-1", "access_key": AccessKey, "secret_key": SecretKey, "path_style": "true",
	}
}
