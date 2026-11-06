package transfer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/internal/tlsroots"
)

// s3MinPartSize is the multipart threshold and the smallest part size: an
// object up to this size is one PUT, a larger one is uploaded in parts. A part
// is buffered in memory while it is sent.
const (
	s3MinPartSize = 64 << 20
	s3MaxParts    = 10000
	// s3CancelWait bounds how long Close waits for an upload to unwind after a
	// cancel. The client aborts a failed multipart upload in the background
	// (up to a minute); the goroutine ends by itself.
	s3CancelWait = 10 * time.Second
)

// partSizeFor grows the part size for very large objects, so the upload stays
// within the 10000 parts S3 allows (64 MiB parts cap an object at ~640 GiB).
// It is rounded up to a whole MiB. Memory per upload is one part.
func partSizeFor(size int64) uint64 {
	part := int64(s3MinPartSize)
	if need := (size + s3MaxParts - 1) / s3MaxParts; need > part {
		part = (need + (1 << 20) - 1) &^ ((1 << 20) - 1)
	}
	return uint64(part)
}

// s3FS serves one bucket. Paths are object keys. Unlike the other protocols it
// writes to the final key (an S3 PUT is atomic), so it has no partial file and
// no rename; see Direct.
type s3FS struct {
	c      *minio.Client
	bucket string
}

// s3Connection holds the fields of the connection secret.
type s3Connection struct {
	endpoint, region, accessKey, secretKey, sessionToken string
	pathStyle                                            bool
	secure                                               bool
}

// parseS3Connection reads the connection secret: endpoint (host[:port] or a
// URL; default s3.amazonaws.com), region, access_key, secret_key, optional
// session_token and path_style ("true"). Errors never contain secret values.
func parseS3Connection(req pluginsdk.ExecRequest, ref string) (s3Connection, error) {
	var cn s3Connection
	if req.Secrets == nil {
		return cn, pluginsdk.NewError(pluginsdk.KindAuth, "no secret resolver available", nil)
	}
	f, err := req.Secrets.ResolveFields(ref)
	if err != nil {
		return cn, pluginsdk.NewError(pluginsdk.KindAuth, "resolving the s3 connection "+ref+" failed", err)
	}
	cn.accessKey, cn.secretKey, cn.sessionToken, cn.region = f["access_key"], f["secret_key"], f["session_token"], f["region"]
	if cn.accessKey == "" || cn.secretKey == "" {
		return cn, pluginsdk.NewError(pluginsdk.KindAuth, "the s3 connection needs access_key and secret_key", nil)
	}
	cn.secure = true
	cn.endpoint = strings.TrimSpace(f["endpoint"])
	if cn.endpoint == "" {
		cn.endpoint = "s3.amazonaws.com"
	}
	if strings.Contains(cn.endpoint, "://") {
		u, perr := url.Parse(cn.endpoint)
		if perr != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || (u.Path != "" && u.Path != "/") {
			return cn, pluginsdk.ValidationError("the s3 connection endpoint must be host[:port] or an http(s) URL without a path")
		}
		cn.endpoint, cn.secure = u.Host, u.Scheme == "https"
	}
	if v := f["path_style"]; v != "" {
		b, perr := strconv.ParseBool(v)
		if perr != nil {
			return cn, pluginsdk.ValidationError("the s3 connection path_style must be true or false")
		}
		cn.pathStyle = b
	}
	return cn, nil
}

func dialS3(req pluginsdk.ExecRequest, e *S3Endpoint, emit pluginsdk.Emitter) (fileSystem, error) {
	cn, err := parseS3Connection(req, e.Connection)
	if err != nil {
		return nil, err
	}
	if !cn.secure {
		emit.Log(pluginsdk.System, "warning: the s3 endpoint "+cn.endpoint+" uses plain http; credentials and data are not encrypted")
	}
	lookup := minio.BucketLookupAuto
	if cn.pathStyle {
		lookup = minio.BucketLookupPath
	}
	opts := &minio.Options{
		Creds:        credentials.NewStaticV4(cn.accessKey, cn.secretKey, cn.sessionToken),
		Secure:       cn.secure,
		Region:       cn.region,
		BucketLookup: lookup,
	}
	if cn.secure {
		tr, terr := s3Transport(req)
		if terr != nil {
			return nil, terr
		}
		opts.Transport = tr
	}
	c, err := minio.New(cn.endpoint, opts)
	if err != nil {
		return nil, pluginsdk.NewError(pluginsdk.KindValidation, "the s3 connection is invalid: "+err.Error(), nil)
	}
	return &s3FS{c: c, bucket: e.Bucket}, nil
}

// s3Transport is the default HTTP transport with the system roots plus the
// operator's extra CA certificates; certificate verification stays on.
func s3Transport(req pluginsdk.ExecRequest) (http.RoundTripper, error) {
	pool, err := tlsroots.Pool(req.Config[ConfigTLSCAPEM])
	if err != nil {
		return nil, pluginsdk.ValidationError("%s %v", ConfigTLSCAPEM, err)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	return tr, nil
}

// Direct tells the engine to write to the final key and skip the rename.
func (*s3FS) Direct() bool { return true }

// s3Error maps an S3 error to a step error without echoing request details.
// Known codes get a precise kind; any other answer is classified by its HTTP
// status: 403 is auth, other 4xx are validation (a retry cannot fix them), and
// 5xx, timeouts, throttling and network errors are connection errors.
func s3Error(err error, what string) error {
	var er minio.ErrorResponse
	if !errors.As(err, &er) {
		return err
	}
	switch er.Code {
	case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch", "ExpiredToken", "InvalidToken", "AllAccessDisabled":
		return pluginsdk.NewError(pluginsdk.KindAuth, what+": "+er.Code, err)
	case "InvalidBucketName", "InvalidObjectName":
		return pluginsdk.NewError(pluginsdk.KindValidation, what+": "+er.Message, err)
	case "NoSuchBucket":
		return pluginsdk.NewError(pluginsdk.KindValidation, what+": the bucket does not exist", err)
	case "PreconditionFailed", "ConditionalRequestConflict":
		return pluginsdk.NewError(pluginsdk.KindValidation, "destination exists (set overwrite to replace it)", err)
	case "SlowDown", "RequestTimeout", "InternalError", "ServiceUnavailable":
		return pluginsdk.NewError(pluginsdk.KindConnection, what+": "+er.Code, err)
	}
	switch {
	case er.StatusCode == http.StatusForbidden:
		return pluginsdk.NewError(pluginsdk.KindAuth, what+": "+er.Code, err)
	case er.StatusCode == http.StatusNotFound:
		return pluginsdk.NewError(pluginsdk.KindValidation, what+": not found", err)
	case er.StatusCode >= 400 && er.StatusCode < 500 && er.StatusCode != http.StatusRequestTimeout && er.StatusCode != http.StatusTooManyRequests:
		return pluginsdk.NewError(pluginsdk.KindValidation, what+": "+er.Code, err)
	}
	return pluginsdk.NewError(pluginsdk.KindConnection, what+": "+er.Code, err)
}

func isNotFound(err error) bool {
	var er minio.ErrorResponse
	return errors.As(err, &er) && (er.Code == "NoSuchKey" || er.StatusCode == http.StatusNotFound && er.Code != "NoSuchBucket")
}

func (s *s3FS) Stat(ctx context.Context, key string) (fileInfo, bool, error) {
	oi, err := s.c.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	switch {
	case isNotFound(err):
		return fileInfo{}, false, nil
	case err != nil:
		return fileInfo{}, false, s3Error(err, "stat "+key)
	}
	return fileInfo{Size: oi.Size}, true, nil
}

func (s *s3FS) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	o, err := s.c.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, s3Error(err, "open "+key)
	}
	return &s3Reader{o: o}, nil
}

// s3Reader maps S3 errors on the first read.
type s3Reader struct{ o *minio.Object }

func (r *s3Reader) Read(p []byte) (int, error) {
	n, err := r.o.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = s3Error(err, "reading the object")
	}
	return n, err
}

func (r *s3Reader) Close() error { return r.o.Close() }

// Create uploads with PutObject, fed through a pipe. Without overwrite the PUT
// carries If-None-Match: *, so S3 itself refuses to replace an object.
func (s *s3FS) Create(ctx context.Context, key string, size int64, overwrite bool) (io.WriteCloser, error) {
	opts := minio.PutObjectOptions{PartSize: partSizeFor(size)}
	if !overwrite {
		opts.SetMatchETagExcept("*")
	}
	pr, pw := io.Pipe()
	w := &s3Writer{ctx: ctx, pw: pw, left: size, done: make(chan error, 1)}
	go func() {
		info, err := s.c.PutObject(ctx, s.bucket, key, pr, size, opts)
		w.mu.Lock()
		w.info = info
		w.mu.Unlock()
		if err == nil {
			err = errTransferEnded
		}
		_ = pr.CloseWithError(err)
		w.done <- err
	}()
	return w, nil
}

type s3Writer struct {
	ctx  context.Context
	pw   *io.PipeWriter
	left int64
	done chan error
	mu   sync.Mutex // guards info: Close may return early after a cancel
	info minio.UploadInfo
}

func (w *s3Writer) Write(b []byte) (int, error) {
	if int64(len(b)) > w.left {
		return 0, errors.New("s3: more data than the announced size (did the source change?)")
	}
	n, err := w.pw.Write(b)
	w.left -= int64(n)
	if err != nil {
		// The pipe carries the upload's own error (access denied at the start
		// of a multipart upload, say); map it so it gets the right kind.
		err = s3Error(err, "uploading")
	}
	return n, err
}

func (w *s3Writer) Close() error {
	_ = w.pw.Close()
	var err error
	select {
	case err = <-w.done:
	case <-w.ctx.Done():
		// A cancelled multipart upload is aborted by the client in the
		// background; wait a bounded time for it, the goroutine ends on its own.
		select {
		case err = <-w.done:
		case <-time.After(s3CancelWait):
			return w.ctx.Err()
		}
	}
	switch {
	case err != nil && !errors.Is(err, errTransferEnded):
		return s3Error(err, "uploading")
	case w.left != 0:
		return errShortWrite
	}
	return nil
}

// ObjectInfo is the ETag and version id S3 returned for the upload.
func (w *s3Writer) ObjectInfo() (etag, versionID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Trim(w.info.ETag, `"`), w.info.VersionID
}

// Mkdirs is a no-op: S3 has no directories.
func (*s3FS) Mkdirs(context.Context, string) error { return nil }

// Rename is never used: Direct filesystems write to the final key.
func (*s3FS) Rename(context.Context, string, string, bool) error {
	return errors.New("s3: rename is not supported")
}

func (s *s3FS) Remove(ctx context.Context, key string) error {
	if err := s.c.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil && !isNotFound(err) {
		return fmt.Errorf("remove %s: %w", key, s3Error(err, "remove "+key))
	}
	return nil
}

// Close has nothing to release: requests use the caller's context, and
// canceling it aborts them.
func (*s3FS) Close() error { return nil }
