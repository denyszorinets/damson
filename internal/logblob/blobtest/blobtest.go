// Package blobtest is the contract suite every logblob.Blob must pass.
package blobtest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/denyszorinets/damson/internal/logblob"
	"github.com/denyszorinets/damson/internal/store"
)

// Run runs the contract against a fresh Blob made by mk.
func Run(t *testing.T, mk func(t *testing.T) logblob.Blob) {
	ctx := context.Background()
	t.Run("round trip and overwrite", func(t *testing.T) {
		b := mk(t)
		bin := []byte{0, 1, 2, 0xff, 0x1f, 0x8b, '\n'}
		if err := b.Put(ctx, "logs/e1/a1/1-2.ndjson", bin); err != nil {
			t.Fatal(err)
		}
		got, err := b.Get(ctx, "logs/e1/a1/1-2.ndjson")
		if err != nil || !bytes.Equal(got, bin) {
			t.Fatalf("get = %v, %v", got, err)
		}
		if err := b.Put(ctx, "logs/e1/a1/1-2.ndjson", []byte("new")); err != nil {
			t.Fatal(err)
		}
		if got, _ := b.Get(ctx, "logs/e1/a1/1-2.ndjson"); string(got) != "new" {
			t.Fatalf("overwrite = %q", got)
		}
	})
	t.Run("missing key", func(t *testing.T) {
		if _, err := mk(t).Get(ctx, "logs/none/x"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("delete prefix is exact", func(t *testing.T) {
		b := mk(t)
		for _, k := range []string{"logs/a/1/1-1.ndjson", "logs/a/2/1-1.ndjson", "logs/ab/1/1-1.ndjson"} {
			if err := b.Put(ctx, k, []byte("x")); err != nil {
				t.Fatal(err)
			}
		}
		n, err := b.DeletePrefix(ctx, "logs/a/")
		if err != nil || n != 2 {
			t.Fatalf("delete = %d, %v", n, err)
		}
		if _, err := b.Get(ctx, "logs/a/1/1-1.ndjson"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("deleted key still there: %v", err)
		}
		if _, err := b.Get(ctx, "logs/ab/1/1-1.ndjson"); err != nil {
			t.Fatalf("sibling prefix lost: %v", err)
		}
		if n, err := b.DeletePrefix(ctx, "logs/a/"); err != nil || n != 0 {
			t.Fatalf("second delete = %d, %v", n, err)
		}
	})
	t.Run("empty prefix", func(t *testing.T) {
		if _, err := mk(t).DeletePrefix(ctx, ""); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("err = %v, want ErrInvalid", err)
		}
	})
}
