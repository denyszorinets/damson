package transfer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"time"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

const (
	partialInfix     = ".damson-partial-"
	progressInterval = time.Second
)

// result is what a finished transfer reports.
type result struct {
	Path   string
	Bytes  int64
	SHA256 string
}

// run copies one file from src to dst and, for move, deletes the source after
// the copy was verified. dstPath is final (trailing slash already applied).
// cleanup deletes a partial file best effort; it must work after cancellation.
// The data is streamed; nothing touches the local disk.
//
// The file is written to <dst>.damson-partial-<random>, verified there, and
// renamed into place, so a failed or cancelled transfer never exposes a
// partial destination. The random suffix keeps a lost attempt that is still
// running on a disconnected agent from clashing with its retry.
func run(ctx context.Context, in Input, srcPath, dstPath string, src, dst fileSystem, emit pluginsdk.Emitter, cleanup func(partial string)) (result, error) {
	srcInfo, err := statSource(ctx, src, srcPath)
	if err != nil {
		return result{}, err
	}
	if err := checkDestination(ctx, in, dst, dstPath); err != nil {
		return result{}, err
	}
	partial, err := partialName(dstPath)
	if err != nil {
		return result{}, err
	}
	res, err := copyToPartial(ctx, in, src, dst, srcPath, partial, srcInfo.Size, emit)
	if err != nil {
		cleanup(partial)
		return result{}, err
	}
	res.Path = dstPath
	if err := dst.Rename(ctx, partial, dstPath, in.Overwrite); err != nil {
		cleanup(partial)
		return result{}, transferError(ctx, err, "moving the verified file into place failed")
	}
	if in.Mode == modeMove {
		if err := src.Remove(ctx, srcPath); err != nil {
			return res, transferError(ctx, err, "copied and verified; source not deleted")
		}
	}
	return res, nil
}

func statSource(ctx context.Context, src fileSystem, p string) (fileInfo, error) {
	fi, ok, err := src.Stat(ctx, p)
	switch {
	case err != nil:
		return fi, transferError(ctx, err, "reading the source failed")
	case !ok:
		return fi, pluginsdk.NewError(pluginsdk.KindValidation, "source not found: "+p, nil)
	case fi.IsDir:
		return fi, pluginsdk.ValidationError("source %s is a directory: a step moves a single file", p)
	}
	return fi, nil
}

// checkDestination fails early when the destination cannot be written. The
// rename without overwrite closes the race between this check and the write.
func checkDestination(ctx context.Context, in Input, dst fileSystem, p string) error {
	fi, exists, err := dst.Stat(ctx, p)
	switch {
	case err != nil:
		return transferError(ctx, err, "checking the destination failed")
	case exists && fi.IsDir:
		return pluginsdk.ValidationError("destination %s is a directory", p)
	case exists && !in.Overwrite:
		return pluginsdk.ValidationError("destination exists: %s (set overwrite to replace it)", p)
	}
	if in.Mkdirs {
		if err := dst.Mkdirs(ctx, path.Dir(p)); err != nil {
			return transferError(ctx, err, "creating the destination directory failed")
		}
	}
	return nil
}

func partialName(dst string) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", pluginsdk.NewError(pluginsdk.KindInternal, "generating a temporary name failed", err)
	}
	return dst + partialInfix + hex.EncodeToString(b[:]), nil
}

// copyToPartial streams the source into the partial file and verifies it.
func copyToPartial(ctx context.Context, in Input, src, dst fileSystem, srcPath, partial string, size int64, emit pluginsdk.Emitter) (result, error) {
	r, err := src.Open(ctx, srcPath)
	if err != nil {
		return result{}, transferError(ctx, err, "opening the source failed")
	}
	defer func() { _ = r.Close() }()
	w, err := dst.Create(ctx, partial)
	if err != nil {
		return result{}, transferError(ctx, err, "creating the destination failed")
	}
	h := sha256.New()
	pr := &progressReader{ctx: ctx, r: io.TeeReader(r, h), total: size, emit: emit}
	n, copyErr := io.Copy(w, pr)
	closeErr := w.Close()
	switch {
	case copyErr != nil:
		return result{}, transferError(ctx, copyErr, "copying failed")
	case closeErr != nil:
		return result{}, transferError(ctx, closeErr, "finishing the destination failed")
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if err := verify(ctx, in.Verify, dst, partial, size, n, sum); err != nil {
		return result{}, err
	}
	return result{Bytes: n, SHA256: sum}, nil
}

// verify checks the partial file before it is renamed into place, so a failed
// check never exposes the destination. sha256 re-reads the written file.
func verify(ctx context.Context, level string, dst fileSystem, partial string, srcSize, streamed int64, sum string) error {
	if level == verifyNone {
		return nil
	}
	if streamed != srcSize {
		return verifyError("the source changed during the copy: %d bytes read, %d expected", streamed, srcSize)
	}
	fi, ok, err := dst.Stat(ctx, partial)
	switch {
	case err != nil:
		return transferError(ctx, err, "verifying the destination failed")
	case !ok || fi.Size != streamed:
		return verifyError("size mismatch: %d bytes written, %d expected", fi.Size, streamed)
	}
	if level != verifySHA256 {
		return nil
	}
	got, err := hashOf(ctx, dst, partial)
	if err != nil {
		return err
	}
	if got != sum {
		return verifyError("sha256 mismatch between the source stream and the written file")
	}
	return nil
}

func hashOf(ctx context.Context, fsys fileSystem, p string) (string, error) {
	r, err := fsys.Open(ctx, p)
	if err != nil {
		return "", transferError(ctx, err, "re-reading the destination failed")
	}
	defer func() { _ = r.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, &ctxReader{ctx: ctx, r: r}); err != nil {
		return "", transferError(ctx, err, "re-reading the destination failed")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func verifyError(format string, args ...any) error {
	return pluginsdk.NewError(pluginsdk.KindInternal, "verification failed: "+fmt.Sprintf(format, args...), nil)
}

// transferError maps err to a step error: a cancelled context wins, an error
// that is already a StepError is kept, anything else is a connection error.
func transferError(ctx context.Context, err error, msg string) error {
	if cerr := ctx.Err(); cerr != nil {
		return pluginsdk.AsStepError(cerr)
	}
	var se *pluginsdk.StepError
	if errors.As(err, &se) {
		return se
	}
	return pluginsdk.NewError(pluginsdk.KindConnection, msg+": "+err.Error(), err)
}

// ctxReader stops a copy as soon as ctx ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// progressReader is a ctxReader that reports progress about once a second.
type progressReader struct {
	ctx   context.Context
	r     io.Reader
	total int64
	emit  pluginsdk.Emitter
	read  int64
	last  time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.r.Read(b)
	p.read += int64(n)
	if p.total > 0 && time.Since(p.last) >= progressInterval {
		p.last = time.Now()
		p.emit.Progress(float64(p.read)/float64(p.total)*100, fmt.Sprintf("%d of %d bytes", p.read, p.total))
	}
	return n, err
}

func itoa(n int64) string { return fmt.Sprint(n) }
