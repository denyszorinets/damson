package s3blob_test

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"go.uber.org/goleak"

	"github.com/denyszorinets/damson/internal/logblob"
	"github.com/denyszorinets/damson/internal/logblob/blobtest"
	"github.com/denyszorinets/damson/internal/logblob/s3blob"
	"github.com/denyszorinets/damson/internal/testutil/fakes3"
)

func TestContract(t *testing.T) {
	for _, prefix := range []string{"", "damson/prod/"} {
		t.Run("prefix="+prefix, func(t *testing.T) {
			blobtest.Run(t, func(t *testing.T) logblob.Blob {
				u, _ := url.Parse(fakes3.Start(t, "logs"))
				s, err := s3blob.New(s3blob.Options{
					Endpoint: u.Host, Bucket: "logs", Prefix: prefix, PathStyle: true,
					AccessKeyID: "AKIATEST", SecretKey: "secret",
				})
				if err != nil {
					t.Fatal(err)
				}
				return s
			})
		})
	}
}

func TestProbe(t *testing.T) {
	u, _ := url.Parse(fakes3.Start(t, "logs"))
	mk := func(bucket string) *s3blob.Store {
		s, err := s3blob.New(s3blob.Options{Endpoint: u.Host, Bucket: bucket, PathStyle: true, AccessKeyID: "a", SecretKey: "b"})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if err := mk("logs").Probe(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := mk("missing").Probe(t.Context()); err == nil {
		t.Fatal("probe of a missing bucket succeeded")
	}
}

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m, goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"), goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"), goleak.IgnoreAnyFunction("internal/poll.runtime_pollWait"))
}

func newStore(t *testing.T, u *url.URL, prefix string) *s3blob.Store {
	t.Helper()
	s, err := s3blob.New(s3blob.Options{Endpoint: u.Host, Bucket: "logs", Prefix: prefix, PathStyle: true, AccessKeyID: "a", SecretKey: "b"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A configured prefix scopes every operation: a store with another prefix on the
// same bucket keeps its objects.
func TestPrefixIsolation(t *testing.T) {
	u, _ := url.Parse(fakes3.Start(t, "logs"))
	a, b := newStore(t, u, ""), newStore(t, u, "damson/prod/")
	ctx := t.Context()
	if err := a.Put(ctx, "logs/e/1", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := b.Put(ctx, "logs/e/1", []byte("b")); err != nil {
		t.Fatal(err)
	}
	if n, err := b.DeletePrefix(ctx, "logs/e/"); err != nil || n != 1 {
		t.Fatalf("delete = %d, %v", n, err)
	}
	if got, err := a.Get(ctx, "logs/e/1"); err != nil || string(got) != "a" {
		t.Fatalf("unprefixed object = %q, %v", got, err)
	}
}

// More than one page of keys (1000) and more than one multi-delete batch.
func TestDeletePrefixPaginates(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	u, _ := url.Parse(fakes3.Start(t, "logs"))
	s := newStore(t, u, "")
	ctx := t.Context()
	const n = 1105
	for i := range n {
		if err := s.Put(ctx, fmt.Sprintf("logs/e/a/%d-%d.ndjson", i, i), []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.DeletePrefix(ctx, "logs/e/")
	if err != nil || got != n {
		t.Fatalf("delete = %d, %v; want %d", got, err, n)
	}
}

// A context that ended must not look like a finished delete.
func TestDeletePrefixCanceled(t *testing.T) {
	u, _ := url.Parse(fakes3.Start(t, "logs"))
	s := newStore(t, u, "")
	if err := s.Put(t.Context(), "logs/e/1", []byte("x")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.DeletePrefix(ctx, "logs/e/"); err == nil {
		t.Fatal("a canceled delete reported success")
	}
	if _, err := s.Get(t.Context(), "logs/e/1"); err != nil {
		t.Fatalf("object lost: %v", err)
	}
}
