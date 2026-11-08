// Package fakes3 is an in-process S3-compatible server for tests of internal
// packages.
package fakes3

import (
	"net/http/httptest"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

// Start runs an http S3 server with the given buckets and returns its host:port
// and URL. The server stops when the test ends.
func Start(t testing.TB, buckets ...string) (url string) {
	t.Helper()
	b := s3mem.New()
	for _, name := range buckets {
		if err := b.CreateBucket(name); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(gofakes3.New(b).Server())
	t.Cleanup(ts.Close)
	return ts.URL
}
