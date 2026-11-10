// Package s3blob stores log chunk objects in an S3-compatible bucket.
package s3blob

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/encrypt"

	"github.com/denyszorinets/damson/internal/store"
)

// MaxObjectBytes bounds what Get reads; a chunk is about 1 MiB.
const MaxObjectBytes = 64 << 20

// Options configures a Store. Its secret fields must never be logged.
type Options struct {
	Endpoint     string // host[:port]
	Secure       bool
	Region       string
	Bucket       string
	Prefix       string // empty, or ends in "/"
	PathStyle    bool
	CAPEM        []byte // extra root certificates; verification stays on
	AccessKeyID  string // empty selects the environment/IAM credential chain
	SecretKey    string
	SessionToken string
	SSE          string // "", "AES256" or "aws:kms"
	SSEKMSKeyID  string
}

// Store implements logblob.Blob over one bucket.
type Store struct {
	c      *minio.Client
	bucket string
	prefix string
	sse    encrypt.ServerSide
}

// New builds a Store. It does not contact the server.
func New(o Options) (*Store, error) {
	lookup := minio.BucketLookupAuto
	if o.PathStyle {
		lookup = minio.BucketLookupPath
	}
	creds := credentials.NewChainCredentials([]credentials.Provider{
		&credentials.EnvAWS{}, &credentials.IAM{},
	})
	if o.AccessKeyID != "" {
		creds = credentials.NewStaticV4(o.AccessKeyID, o.SecretKey, o.SessionToken)
	}
	opts := &minio.Options{Creds: creds, Secure: o.Secure, Region: o.Region, BucketLookup: lookup}
	if o.Secure {
		tr, err := transport(o.CAPEM)
		if err != nil {
			return nil, err
		}
		opts.Transport = tr
	}
	c, err := minio.New(o.Endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("creating the s3 log client: %w", err)
	}
	s := &Store{c: c, bucket: o.Bucket, prefix: o.Prefix}
	switch o.SSE {
	case "":
	case "AES256":
		s.sse = encrypt.NewSSE()
	case "aws:kms":
		if s.sse, err = encrypt.NewSSEKMS(o.SSEKMSKeyID, nil); err != nil {
			return nil, fmt.Errorf("configuring s3 kms encryption: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown s3 sse %q", o.SSE)
	}
	return s, nil
}

func transport(caPEM []byte) (http.RoundTripper, error) {
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default transport is not an *http.Transport")
	}
	tr = tr.Clone()
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if len(caPEM) > 0 && !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("logs.s3.tls_ca_file holds no certificate")
	}
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	return tr, nil
}

// Probe checks that the bucket exists and is reachable.
func (s *Store) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ok, err := s.c.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("checking s3 log bucket %q: %w", s.bucket, err)
	}
	if !ok {
		return fmt.Errorf("s3 log bucket %q does not exist", s.bucket)
	}
	return nil
}

func (s *Store) key(k string) string { return s.prefix + k }

// Put implements logblob.Blob.
func (s *Store) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.c.PutObject(ctx, s.bucket, s.key(key), bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: "application/x-ndjson", DisableMultipart: true, ServerSideEncryption: s.sse,
	})
	if err != nil {
		return fmt.Errorf("putting s3 log object %q: %w", key, err)
	}
	return nil
}

// Get implements logblob.Blob.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	o, err := s.c.GetObject(ctx, s.bucket, s.key(key), minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting s3 log object %q: %w", key, err)
	}
	defer func() { _ = o.Close() }()
	data, err := io.ReadAll(io.LimitReader(o, MaxObjectBytes+1))
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, fmt.Errorf("s3 log object %q: %w", key, store.ErrNotFound)
		}
		return nil, fmt.Errorf("reading s3 log object %q: %w", key, err)
	}
	if len(data) > MaxObjectBytes {
		return nil, fmt.Errorf("s3 log object %q is larger than %d bytes", key, MaxObjectBytes)
	}
	return data, nil
}

// DeletePrefix implements logblob.Blob.
func (s *Store) DeletePrefix(ctx context.Context, prefix string) (int64, error) {
	if prefix == "" {
		return 0, fmt.Errorf("an empty log prefix: %w", store.ErrInvalid)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	in := make(chan minio.ObjectInfo)
	done := make(chan struct{})
	var listErr error
	var listed int64
	go func() {
		defer close(done)
		defer close(in)
		list := s.c.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: s.key(prefix), Recursive: true})
		// Drain the listing on every exit: minio's producer blocks on an unread
		// channel after a cancel and would leak.
		defer func() {
			for range list {
			}
		}()
		for o := range list {
			if o.Err != nil {
				listErr = o.Err
				return
			}
			select {
			case in <- o:
				listed++
			case <-ctx.Done():
				return
			}
		}
	}()
	var failed int64
	var firstErr error
	for r := range s.c.RemoveObjects(ctx, s.bucket, in, minio.RemoveObjectsOptions{}) {
		if r.Err != nil {
			failed++
			if firstErr == nil {
				firstErr = r.Err
			}
		}
	}
	<-done
	// minio ends a listing silently when the context ends between pages, so a
	// cut-short delete must not look complete.
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("deleting s3 log objects under %q: %w", prefix, err)
	}
	if listErr != nil {
		return 0, fmt.Errorf("listing s3 log objects under %q: %w", prefix, listErr)
	}
	if firstErr != nil {
		return 0, fmt.Errorf("deleting %d s3 log objects under %q: %w", failed, prefix, firstErr)
	}
	return listed, nil
}
