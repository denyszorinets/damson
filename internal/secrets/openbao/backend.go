package openbao

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/denyszorinets/damson/internal/secrets"
	"github.com/denyszorinets/damson/internal/store"
)

// custom_metadata keys Damson owns; others are kept as they are.
const (
	metaType     = "damson_type"
	metaFieldPfx = "damson_field_"
)

// maxAttempts bounds retries of an unconditional write that races another.
const maxAttempts = 3

// Backend reads and writes secrets in a KV v2 mount. Its methods match the db
// backend's, so the node, the API and the dev seeder use either.
type Backend struct {
	c     *Client
	audit store.AuditStore
	now   func() time.Time
	cache *cache
}

// NewBackend returns a Backend over c. Events about writes go to audit.
func NewBackend(c *Client, audit store.AuditStore, now func() time.Time) *Backend {
	b := &Backend{c: c, audit: audit, now: now}
	if c.cfg.CacheTTL > 0 {
		b.cache = newCache(c.cfg.CacheTTL, now)
	}
	return b
}

// Start starts the client and the cache sweeper.
func (b *Backend) Start(ctx context.Context) error {
	if err := b.c.Start(ctx); err != nil {
		return err
	}
	if b.cache != nil {
		b.cache.start(ctx)
	}
	return nil
}

// Stop stops what Start started.
func (b *Backend) Stop(ctx context.Context) error {
	if b.cache != nil {
		b.cache.stop(ctx)
	}
	return b.c.Stop(ctx)
}

// path renders the API path of kind ("data" or "metadata") for a secret; an
// empty name addresses the scope folder.
func (b *Backend) path(kind, scope, name string) string {
	parts := []string{b.c.cfg.Mount, kind}
	if p := b.c.cfg.Prefix; p != "" {
		parts = append(parts, strings.Split(p, "/")...)
	}
	parts = append(parts, strings.Split(scope, "/")...)
	if name != "" {
		parts = append(parts, name)
	}
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

type kvMeta struct {
	CurrentVersion int64             `json:"current_version"`
	CreatedTime    time.Time         `json:"created_time"`
	UpdatedTime    time.Time         `json:"updated_time"`
	CustomMetadata map[string]string `json:"custom_metadata"`
	Versions       map[string]struct {
		DeletionTime string `json:"deletion_time"`
		Destroyed    bool   `json:"destroyed"`
	} `json:"versions"`
}

// current reports whether the current version still has data.
func (m kvMeta) current() bool {
	if m.CurrentVersion <= 0 {
		return false // metadata only, such as `bao kv metadata put` before any data
	}
	v, ok := m.Versions[fmt.Sprint(m.CurrentVersion)]
	return !ok || (v.DeletionTime == "" && !v.Destroyed)
}

func (m kvMeta) toMeta(scope, name string) store.SecretMeta {
	typ := m.CustomMetadata[metaType]
	if typ == "" {
		typ = "generic" // written outside Damson, or a crash between data and metadata
	}
	var fields []string
	for k := range m.CustomMetadata {
		if f, ok := strings.CutPrefix(k, metaFieldPfx); ok {
			fields = append(fields, f)
		}
	}
	sort.Strings(fields)
	return store.SecretMeta{
		Scope: scope, Name: name, Type: typ, CurrentVersion: m.CurrentVersion, FieldNames: fields,
		CreatedAt: m.CreatedTime.UTC(), UpdatedAt: m.UpdatedTime.UTC(),
	}
}

// readMeta returns the metadata of scope/name, or secrets.ErrNotFound.
func (b *Backend) readMeta(ctx context.Context, scope, name string) (kvMeta, error) {
	var out struct {
		Data kvMeta `json:"data"`
	}
	err := b.c.do(ctx, http.MethodGet, b.path("metadata", scope, name), nil, &out, true)
	if statusOf(err) == http.StatusNotFound {
		return kvMeta{}, fmt.Errorf("secret %s/%s: %w", scope, name, secrets.ErrNotFound)
	}
	if err != nil {
		return kvMeta{}, b.wrap(scope, name, err)
	}
	return out.Data, nil
}

// wrap names the secret in a backend error and classifies a rejection.
func (b *Backend) wrap(scope, name string, err error) error {
	if statusOf(err) == http.StatusForbidden || errors.Is(err, errMalformed) {
		return fmt.Errorf("secret %s/%s: %w: %w", scope, name, secrets.ErrUnavailable, err)
	}
	return fmt.Errorf("secret %s/%s: %w", scope, name, err)
}

// Meta returns the metadata of one secret.
func (b *Backend) Meta(ctx context.Context, scope, name string) (store.SecretMeta, error) {
	if err := validate(scope, name); err != nil {
		return store.SecretMeta{}, err
	}
	m, err := b.readMeta(ctx, scope, name)
	if err != nil {
		return store.SecretMeta{}, err
	}
	if !m.current() {
		return store.SecretMeta{}, fmt.Errorf("secret %s/%s: %w", scope, name, secrets.ErrNotFound)
	}
	return m.toMeta(scope, name), nil
}

func validate(scope, name string) error {
	if err := secrets.ValidateScope(scope); err != nil {
		return err
	}
	return secrets.ValidateName(name)
}

// Get returns the current version's fields. The caller owns the map and should
// clear it when done. A value that is not a string, or a field the grammar
// refuses, makes the secret unavailable (a permanent failure).
func (b *Backend) Get(ctx context.Context, scope, name string) (map[string]string, error) {
	if err := validate(scope, name); err != nil {
		return nil, err
	}
	var out struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	err := b.c.do(ctx, http.MethodGet, b.path("data", scope, name), nil, &out, true)
	if statusOf(err) == http.StatusNotFound {
		return nil, fmt.Errorf("secret %s/%s: %w", scope, name, secrets.ErrNotFound)
	}
	if err != nil {
		return nil, b.wrap(scope, name, err)
	}
	fields, err := decodeFields(out.Data.Data)
	clear(out.Data.Data)
	if err != nil {
		return nil, fmt.Errorf("secret %s/%s: %w: %w", scope, name, secrets.ErrUnavailable, err)
	}
	return fields, nil
}

// decodeFields checks what OpenBao returned against the secret limits. The
// error names no value.
func decodeFields(raw map[string]any) (map[string]string, error) {
	fields := make(map[string]string, len(raw))
	for k, v := range raw {
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("a field is not a string")
		}
		fields[k] = s
	}
	t, _ := secrets.LookupType("generic")
	if err := t.ValidateFields(fields); err != nil {
		clear(fields)
		return nil, err
	}
	return fields, nil
}

// Put creates a secret or adds a version. ifVersion nil writes whatever the
// current state is; 0 requires that the secret does not exist; n requires
// current version n. created reports whether the secret is new.
func (b *Backend) Put(ctx context.Context, scope, name, typ string, fields map[string]string, ifVersion *int64) (store.SecretMeta, bool, error) {
	if b.c.cfg.ReadOnly {
		return store.SecretMeta{}, false, secrets.ErrReadOnly
	}
	if err := validate(scope, name); err != nil {
		return store.SecretMeta{}, false, err
	}
	t, err := secrets.LookupType(typ)
	if err != nil {
		return store.SecretMeta{}, false, err
	}
	if err := t.ValidateFields(fields); err != nil {
		return store.SecretMeta{}, false, err
	}
	if _, err := store.AuditFrom(ctx); err != nil { // no actor, no write
		return store.SecretMeta{}, false, err
	}
	// A write may have landed even when it reports an error, so the cache entry
	// goes after every attempt.
	defer b.cache.forget(scope, name)
	for range maxAttempts {
		meta, created, err := b.putOnce(ctx, scope, name, typ, fields, ifVersion)
		if errors.Is(err, errRetry) {
			continue
		}
		var partial *partialWrite
		if errors.As(err, &partial) {
			// The data version exists: record it, then report the failure.
			if rerr := b.record(ctx, store.ActionSecretUpdate, scope, name, store.SecretAuditDetails{Type: typ, Version: partial.version, Fields: partial.fields}); rerr != nil {
				return store.SecretMeta{}, false, errors.Join(err, rerr)
			}
			return store.SecretMeta{}, false, err
		}
		if err != nil {
			return store.SecretMeta{}, false, err
		}
		action := store.ActionSecretUpdate
		if created {
			action = store.ActionSecretCreate
		}
		return meta, created, b.record(ctx, action, scope, name, store.SecretAuditDetails{Type: typ, Version: meta.CurrentVersion, Fields: meta.FieldNames})
	}
	return store.SecretMeta{}, false, fmt.Errorf("secret %s/%s: %w", scope, name, secrets.ErrVersionConflict)
}

var errRetry = errors.New("retry")

// partialWrite is a Put whose data version landed but whose metadata did not.
type partialWrite struct {
	version int64
	fields  []string
	err     error
}

func (p *partialWrite) Error() string { return p.err.Error() }
func (p *partialWrite) Unwrap() error { return p.err }

func (b *Backend) putOnce(ctx context.Context, scope, name, typ string, fields map[string]string, ifVersion *int64) (store.SecretMeta, bool, error) {
	cur, err := b.readMeta(ctx, scope, name)
	exists := err == nil
	switch {
	case errors.Is(err, secrets.ErrNotFound):
		if ifVersion != nil && *ifVersion != 0 {
			return store.SecretMeta{}, false, err
		}
	case err != nil:
		return store.SecretMeta{}, false, err
	default:
		live := cur.current()
		if live && ifVersion != nil && *ifVersion != cur.CurrentVersion {
			return store.SecretMeta{}, false, fmt.Errorf("secret %s/%s: %w", scope, name, secrets.ErrVersionConflict)
		}
		if !live && ifVersion != nil && *ifVersion != 0 {
			return store.SecretMeta{}, false, fmt.Errorf("secret %s/%s: %w", scope, name, secrets.ErrNotFound)
		}
		if have := cur.CustomMetadata[metaType]; live && have != "" && have != typ {
			return store.SecretMeta{}, false, fmt.Errorf("secret %s/%s has type %s: %w", scope, name, have, secrets.ErrTypeChange)
		}
		exists = live
	}
	data := make(map[string]any, len(fields))
	for k, v := range fields {
		data[k] = v
	}
	defer clear(data)
	body := map[string]any{"options": map[string]any{"cas": cur.CurrentVersion}, "data": data}
	var out struct {
		Data struct {
			Version     int64     `json:"version"`
			CreatedTime time.Time `json:"created_time"`
		} `json:"data"`
	}
	if err := b.c.do(ctx, http.MethodPost, b.path("data", scope, name), body, &out, false); err != nil {
		if statusOf(err) == http.StatusBadRequest && strings.Contains(strings.ToLower(errMsg(err)), "check-and-set") {
			if ifVersion != nil {
				return store.SecretMeta{}, false, fmt.Errorf("secret %s/%s: %w", scope, name, secrets.ErrVersionConflict)
			}
			return store.SecretMeta{}, false, errRetry
		}
		return store.SecretMeta{}, false, b.wrap(scope, name, err)
	}
	names := make([]string, 0, len(fields))
	custom := map[string]string{}
	for k, v := range cur.CustomMetadata {
		if !strings.HasPrefix(k, "damson_") {
			custom[k] = v
		}
	}
	custom[metaType] = typ
	for k := range fields {
		custom[metaFieldPfx+k] = "1"
		names = append(names, k)
	}
	sort.Strings(names)
	if err := b.c.do(ctx, http.MethodPost, b.path("metadata", scope, name), map[string]any{"custom_metadata": custom}, nil, false); err != nil {
		return store.SecretMeta{}, false, &partialWrite{version: out.Data.Version, fields: names, err: fmt.Errorf("the secret was written but its metadata was not: %w", b.wrap(scope, name, err))}
	}
	now := b.now()
	created := out.Data.CreatedTime
	if created.IsZero() {
		created = now
	}
	return store.SecretMeta{
		Scope: scope, Name: name, Type: typ, CurrentVersion: out.Data.Version, FieldNames: names,
		CreatedAt: createdAt(cur, exists, created), UpdatedAt: now.UTC(),
	}, !exists, nil
}

func createdAt(cur kvMeta, exists bool, written time.Time) time.Time {
	if exists && !cur.CreatedTime.IsZero() {
		return cur.CreatedTime.UTC()
	}
	return written.UTC()
}

func errMsg(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Msg
	}
	return ""
}

// Delete removes a secret and all its versions.
func (b *Backend) Delete(ctx context.Context, scope, name string) error {
	if b.c.cfg.ReadOnly {
		return secrets.ErrReadOnly
	}
	if err := validate(scope, name); err != nil {
		return err
	}
	if _, err := store.AuditFrom(ctx); err != nil {
		return err
	}
	defer b.cache.forget(scope, name)
	cur, err := b.readMeta(ctx, scope, name)
	if err != nil {
		return err
	}
	if err := b.c.do(ctx, http.MethodDelete, b.path("metadata", scope, name), nil, nil, false); err != nil {
		return b.wrap(scope, name, err)
	}
	return b.record(ctx, store.ActionSecretDelete, scope, name, store.SecretAuditDetails{Type: cur.toMeta(scope, name).Type, Version: cur.CurrentVersion})
}

// record appends the audit event after the change. The change lives in
// another system, so it cannot share a transaction (ADR-0068).
func (b *Backend) record(ctx context.Context, action, scope, name string, d store.SecretAuditDetails) error {
	ev, err := store.NewSecretEvent(ctx, action, scope, name, d, b.now())
	if err != nil {
		return err
	}
	if err := b.audit.AppendAuditEvent(ctx, ev); err != nil {
		b.c.log.Error("a secret changed in openbao but its audit event was not recorded", "action", action, "scope", scope, "name", name, "actor", ev.Actor, "error", err)
		return fmt.Errorf("the secret changed but the audit event was not recorded: %w", err)
	}
	return nil
}

// GetForDispatch is Get through the cache, for the dispatch path only. The
// dispatcher authorizes the ref before it asks.
func (b *Backend) GetForDispatch(ctx context.Context, scope, name string) (map[string]string, error) {
	if b.cache == nil {
		return b.Get(ctx, scope, name)
	}
	return b.cache.get(ctx, scope, name, b.Get)
}
