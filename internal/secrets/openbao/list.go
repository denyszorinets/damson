package openbao

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/denyszorinets/damson/internal/secrets"
	"github.com/denyszorinets/damson/internal/store"
)

const (
	maxListedKeys  = 10000
	metaConcurrent = 8
)

type listed struct{ scope, name string }

// List returns metadata, never values, ordered by (scope, name) after the
// cursor. An empty scope lists every scope below the prefix. Only the page is
// read for metadata; the folders are walked in full, so an install with more
// than maxListedKeys secrets gets an error instead of a partial answer.
func (b *Backend) List(ctx context.Context, scope, afterScope, afterName string, limit int) ([]store.SecretMeta, error) {
	if scope != "" {
		if err := secrets.ValidateScope(scope); err != nil {
			return nil, err
		}
	}
	var all []listed
	count := 0
	if err := b.walk(ctx, scope, &all, &count, 0, scope == ""); err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].scope != all[j].scope {
			return all[i].scope < all[j].scope
		}
		return all[i].name < all[j].name
	})
	limit = store.ClampLimit(limit)
	var rest []listed
	for _, e := range all {
		if afterScope != "" || afterName != "" {
			if e.scope < afterScope || (e.scope == afterScope && e.name <= afterName) {
				continue
			}
		}
		rest = append(rest, e)
	}
	// Entries that are no longer live (soft-deleted, metadata only) are dropped
	// after their metadata is read, so keep reading until the page is full or the
	// list runs out: a short page would end the caller's paging early.
	out := make([]store.SecretMeta, 0, limit)
	for len(out) < limit && len(rest) > 0 {
		n := min(limit-len(out), len(rest))
		got, err := b.metas(ctx, rest[:n])
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
		rest = rest[n:]
	}
	return out, nil
}

// walk lists the folder of scope ("" is the prefix) and, with recurse, its
// subfolders.
func (b *Backend) walk(ctx context.Context, scope string, out *[]listed, count *int, depth int, recurse bool) error {
	if depth > 9 {
		return nil
	}
	keys, err := b.listFolder(ctx, scope)
	if err != nil {
		return err
	}
	for _, k := range keys {
		*count++
		if *count > maxListedKeys {
			return fmt.Errorf("more than %d entries under the secret prefix: list a project scope instead", maxListedKeys)
		}
		if folder, ok := strings.CutSuffix(k, "/"); ok {
			if !recurse {
				continue
			}
			sub := folder
			if scope != "" {
				sub = scope + "/" + folder
			}
			if scope == "" && sub == "" {
				continue
			}
			if secrets.ValidateScope(sub) != nil {
				continue // not written by Damson
			}
			if err := b.walk(ctx, sub, out, count, depth+1, true); err != nil {
				return err
			}
			continue
		}
		if scope == "" || secrets.ValidateName(k) != nil {
			continue // a secret directly under the prefix has no scope
		}
		*out = append(*out, listed{scope, k})
	}
	return nil
}

// listFolder runs a KV LIST on a folder; a missing folder is empty.
func (b *Backend) listFolder(ctx context.Context, scope string) ([]string, error) {
	p := b.folderPath(scope)
	var out struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	err := b.c.do(ctx, "LIST", p, nil, &out, true)
	if statusOf(err) == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		if statusOf(err) == http.StatusForbidden {
			return nil, fmt.Errorf("listing secrets: %w: %w", secrets.ErrUnavailable, err)
		}
		return nil, fmt.Errorf("listing secrets: %w", err)
	}
	return out.Data.Keys, nil
}

func (b *Backend) folderPath(scope string) string {
	if scope == "" {
		p := b.c.cfg.Mount + "/metadata"
		if b.c.cfg.Prefix != "" {
			p += "/" + b.c.cfg.Prefix
		}
		return p + "/"
	}
	return b.path("metadata", scope, "") + "/"
}

// metas reads the metadata of the page, a few at a time. A secret deleted since
// the listing is dropped.
func (b *Backend) metas(ctx context.Context, page []listed) ([]store.SecretMeta, error) {
	out := make([]store.SecretMeta, len(page))
	ok := make([]bool, len(page))
	errs := make([]error, len(page))
	sem := make(chan struct{}, metaConcurrent)
	var wg sync.WaitGroup
	for i, e := range page {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			m, err := b.Meta(ctx, e.scope, e.name)
			switch {
			case errors.Is(err, secrets.ErrNotFound):
			case err != nil:
				errs[i] = err
			default:
				out[i], ok[i] = m, true
			}
		}()
	}
	wg.Wait()
	res := make([]store.SecretMeta, 0, len(page))
	for i := range page {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if ok[i] {
			res = append(res, out[i])
		}
	}
	return res, nil
}
