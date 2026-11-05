package db_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/secrets"
	"github.com/denyszorinets/damson/internal/secrets/db"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite"
)

var now = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func newStore(t *testing.T) store.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "damson.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func key(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func newBackend(t *testing.T, s store.Store, k string) *db.Backend {
	t.Helper()
	ring, err := secrets.NewKeyring(k, "")
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int64
	return db.New(s, ring, rand.Reader, func() time.Time { return now }, func() string { return fmt.Sprintf("id%d", n.Add(1)) })
}

func i64(n int64) *int64 { return &n }

func TestPutGetFlow(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	b := newBackend(t, s, key(t))
	pw := map[string]string{"username": "root", "password": "hunter2-value"}

	m, created, err := b.Put(ctx, "proj", "db", "password", pw, nil)
	if err != nil || !created || m.CurrentVersion != 1 {
		t.Fatalf("create: %+v %v %v", m, created, err)
	}
	if got := strings.Join(m.FieldNames, ","); got != "password,username" {
		t.Errorf("field names = %s", got)
	}
	m, created, err = b.Put(ctx, "proj", "db", "password", map[string]string{"password": "second"}, nil)
	if err != nil || created || m.CurrentVersion != 2 {
		t.Fatalf("new version: %+v %v %v", m, created, err)
	}
	got, err := b.Get(ctx, "proj", "db")
	if err != nil || got["password"] != "second" || len(got) != 1 {
		t.Fatalf("Get: %v %v", got, err)
	}

	// Conditional writes.
	if _, _, err := b.Put(ctx, "proj", "db", "password", pw, i64(0)); !errors.Is(err, secrets.ErrVersionConflict) {
		t.Errorf("if_version 0 on existing: %v", err)
	}
	if _, _, err := b.Put(ctx, "proj", "db", "password", pw, i64(1)); !errors.Is(err, secrets.ErrVersionConflict) {
		t.Errorf("if_version stale: %v", err)
	}
	if _, _, err := b.Put(ctx, "proj", "nope", "password", pw, i64(3)); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("if_version on missing: %v", err)
	}
	if _, created, err := b.Put(ctx, "proj", "new", "password", pw, i64(0)); err != nil || !created {
		t.Errorf("if_version 0 create: %v %v", created, err)
	}
	if m, _, err := b.Put(ctx, "proj", "db", "password", pw, i64(2)); err != nil || m.CurrentVersion != 3 {
		t.Errorf("if_version match: %+v %v", m, err)
	}
	// Type cannot change.
	if _, _, err := b.Put(ctx, "proj", "db", "token", map[string]string{"token": "x"}, nil); !errors.Is(err, secrets.ErrTypeChange) {
		t.Errorf("type change: %v", err)
	}
	// Validation.
	if _, _, err := b.Put(ctx, "_proj", "x", "generic", pw, nil); !errors.Is(err, secrets.ErrBadRef) {
		t.Errorf("bad scope: %v", err)
	}
	if _, _, err := b.Put(ctx, "proj", "x", "nope", pw, nil); !errors.Is(err, secrets.ErrBadType) {
		t.Errorf("bad type: %v", err)
	}
	if _, _, err := b.Put(ctx, "proj", "x", "password", map[string]string{"username": "u"}, nil); !errors.Is(err, secrets.ErrBadFields) {
		t.Errorf("missing field: %v", err)
	}
	if _, err := b.Get(ctx, "proj", "missing"); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("Get missing: %v", err)
	}
	if err := b.Delete(ctx, "proj", "db"); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(ctx, "proj", "db"); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("delete again: %v", err)
	}
}

func TestStoredFormHasNoPlaintext(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	b := newBackend(t, s, key(t))
	const val = "plaintext-marker-value"
	if _, _, err := b.Put(ctx, "proj", "x", "generic", map[string]string{"k": val}, nil); err != nil {
		t.Fatal(err)
	}
	_, blob, _ := s.GetSecret(ctx, "proj", "x")
	for name, part := range map[string][]byte{"ct": blob.Ciphertext, "dek": blob.WrappedDEK, "nonce": blob.Nonce} {
		if bytes.Contains(part, []byte(val)) {
			t.Errorf("%s holds the plaintext", name)
		}
	}
	list, _ := b.List(ctx, "", "", "", 10)
	if len(list) != 1 || strings.Contains(fmt.Sprintf("%+v", list), val) {
		t.Errorf("list = %+v", list)
	}
}

func TestGetWithWrongKeyFails(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if _, _, err := newBackend(t, s, key(t)).Put(ctx, "p", "x", "generic", map[string]string{"k": "v"}, nil); err != nil {
		t.Fatal(err)
	}
	_, err := newBackend(t, s, key(t)).Get(ctx, "p", "x")
	if !errors.Is(err, secrets.ErrUnknownKEK) {
		t.Fatalf("err = %v, want ErrUnknownKEK", err)
	}
}

func TestPutRaceUnconditional(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	b := newBackend(t, s, key(t))
	errc := make(chan error, 4)
	for i := range 4 {
		go func() {
			_, _, err := b.Put(ctx, "p", "x", "generic", map[string]string{"k": fmt.Sprint(i)}, nil)
			errc <- err
		}()
	}
	for range 4 {
		if err := <-errc; err != nil && !errors.Is(err, secrets.ErrVersionConflict) {
			t.Errorf("put: %v", err)
		}
	}
	m, _ := b.Meta(ctx, "p", "x")
	if m.CurrentVersion < 2 {
		t.Errorf("version = %d; want writers to stack versions", m.CurrentVersion)
	}
}

func TestCheckKEK(t *testing.T) {
	ctx := context.Background()
	ringOf := func(a, p string) *secrets.Keyring {
		r, err := secrets.NewKeyring(a, p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	a, c := key(t), key(t)

	s := newStore(t)
	if st, err := db.CheckKEK(ctx, s, nil, rand.Reader); err != nil || st.Enabled {
		t.Fatalf("no KEK, no canary: %+v %v", st, err)
	}
	if _, err := s.GetSetting(ctx, db.CanaryKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("canary created without a KEK")
	}
	if st, err := db.CheckKEK(ctx, s, ringOf(a, ""), rand.Reader); err != nil || !st.Enabled || st.RotateDue {
		t.Fatalf("first start: %+v %v", st, err)
	}
	if st, err := db.CheckKEK(ctx, s, ringOf(a, ""), rand.Reader); err != nil || !st.Enabled {
		t.Fatalf("restart: %+v %v", st, err)
	}
	if _, err := db.CheckKEK(ctx, s, nil, rand.Reader); !errors.Is(err, db.ErrKEK) {
		t.Fatalf("no KEK with a canary: %v; want ErrKEK", err)
	}
	_, err := db.CheckKEK(ctx, s, ringOf(c, ""), rand.Reader)
	if !errors.Is(err, db.ErrKEK) || strings.Contains(err.Error(), a) || strings.Contains(err.Error(), c) {
		t.Fatalf("wrong KEK: %v (must fail and not print keys)", err)
	}
	if st, err := db.CheckKEK(ctx, s, ringOf(c, a), rand.Reader); err != nil || !st.Enabled || !st.RotateDue {
		t.Fatalf("previous key: %+v %v", st, err)
	}
}

func TestCheckKEKRace(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	res := make(chan error, 2)
	for _, k := range []string{key(t), key(t)} {
		ring, _ := secrets.NewKeyring(k, "")
		go func() { _, err := db.CheckKEK(ctx, s, ring, rand.Reader); res <- err }()
	}
	e1, e2 := <-res, <-res
	if (e1 == nil) == (e2 == nil) {
		t.Fatalf("exactly one controller may start: %v / %v", e1, e2)
	}
}
