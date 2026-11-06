package storetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

func blob(tag string) store.SecretBlob {
	return store.SecretBlob{
		KEKID: "kek1", WrappedDEK: []byte("w-" + tag), Nonce: []byte("n-" + tag),
		Ciphertext: []byte("c-" + tag), FieldNames: []string{"password", "username"},
	}
}

func testSecrets(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)

	m, err := s.CreateSecret(ctx, "s1", "proj/a", "db", "password", blob("1"), t0)
	if err != nil || m.CurrentVersion != 1 || m.Type != "password" || len(m.FieldNames) != 2 || !m.CreatedAt.Equal(t0) {
		t.Fatalf("CreateSecret = %+v, %v", m, err)
	}
	if _, err := s.CreateSecret(ctx, "s2", "proj/a", "db", "password", blob("x"), t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate: %v; want ErrConflict", err)
	}
	// Same name in another scope is a different secret.
	if _, err := s.CreateSecret(ctx, "s3", "proj/b", "db", "token", blob("3"), t0); err != nil {
		t.Fatalf("other scope: %v", err)
	}

	gm, gb, err := s.GetSecret(ctx, "proj/a", "db")
	if err != nil || gm.ID != "s1" || !bytes.Equal(gb.Ciphertext, []byte("c-1")) || gb.KEKID != "kek1" ||
		!bytes.Equal(gb.WrappedDEK, []byte("w-1")) || !bytes.Equal(gb.Nonce, []byte("n-1")) {
		t.Fatalf("GetSecret = %+v %+v, %v", gm, gb, err)
	}
	if _, _, err := s.GetSecret(ctx, "proj/a", "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}

	t1 := t0.Add(time.Minute)
	m, err = s.AddSecretVersion(ctx, "s1", 1, blob("2"), t1)
	if err != nil || m.CurrentVersion != 2 || !m.UpdatedAt.Equal(t1) || !m.CreatedAt.Equal(t0) {
		t.Fatalf("AddSecretVersion = %+v, %v", m, err)
	}
	if _, gb, _ := s.GetSecret(ctx, "proj/a", "db"); !bytes.Equal(gb.Ciphertext, []byte("c-2")) {
		t.Fatalf("current is not the new version: %q", gb.Ciphertext)
	}
	if _, err := s.AddSecretVersion(ctx, "s1", 1, blob("stale"), t1); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale: %v; want ErrStale", err)
	}
	if _, err := s.AddSecretVersion(ctx, "no-such-id", 1, blob("x"), t1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("add to missing: %v; want ErrNotFound", err)
	}

	// Only SecretVersionsKept versions survive.
	for v := int64(2); v < 20; v++ {
		if _, err := s.AddSecretVersion(ctx, "s1", v, blob(fmt.Sprint(v+1)), t1); err != nil {
			t.Fatalf("version %d: %v", v+1, err)
		}
	}
	if m, _ := s.GetSecretMeta(ctx, "proj/a", "db"); m.CurrentVersion != 20 {
		t.Fatalf("version = %d, want 20", m.CurrentVersion)
	}

	// Listing: ordered keyset pages, scope filter, no encrypted material.
	if _, err := s.CreateSecret(ctx, "s4", "proj/a", "zz", "generic", blob("4"), t0); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListSecrets(ctx, "", "", "", 10)
	if err != nil || len(all) != 3 || all[0].Name != "db" || all[0].Scope != "proj/a" || all[1].Name != "zz" || all[2].Scope != "proj/b" {
		t.Fatalf("ListSecrets = %+v, %v", all, err)
	}
	page, _ := s.ListSecrets(ctx, "", all[0].Scope, all[0].Name, 1)
	if len(page) != 1 || page[0].Name != "zz" {
		t.Fatalf("page 2 = %+v", page)
	}
	if only, _ := s.ListSecrets(ctx, "proj/b", "", "", 10); len(only) != 1 || only[0].Scope != "proj/b" {
		t.Fatalf("scope filter = %+v", only)
	}

	if err := s.DeleteSecret(ctx, "proj/a", "db"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if err := s.DeleteSecret(ctx, "proj/a", "db"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete again: %v", err)
	}
	if _, _, err := s.GetSecret(ctx, "proj/a", "db"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	// A deleted name can be created again, starting at version 1.
	if m, err := s.CreateSecret(ctx, "s5", "proj/a", "db", "token", blob("5"), t0); err != nil || m.CurrentVersion != 1 {
		t.Fatalf("recreate: %+v %v", m, err)
	}
}

// testSecretsRaces: concurrent creates yield one winner; concurrent version
// adds at the same expected version yield one winner.
func testSecretsRaces(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	var wins, conflicts atomic.Int32
	var winner atomic.Value
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CreateSecret(ctx, fmt.Sprintf("r%d", i), "race", "x", "generic", blob("c"), t0)
			switch {
			case err == nil:
				wins.Add(1)
				winner.Store(fmt.Sprintf("r%d", i))
			case errors.Is(err, store.ErrConflict):
				conflicts.Add(1)
			default:
				t.Errorf("create: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || conflicts.Load() != 5 {
		t.Fatalf("create race: %d wins, %d conflicts", wins.Load(), conflicts.Load())
	}
	wins.Store(0)
	var stale atomic.Int32
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.AddSecretVersion(ctx, winner.Load().(string), 1, blob("v"), t0)
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, store.ErrStale):
				stale.Add(1)
			default:
				t.Errorf("add: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || stale.Load() != 5 {
		t.Fatalf("version race: %d wins, %d stale", wins.Load(), stale.Load())
	}
}

// A delete racing version adds, with enough versions that pruning runs, ends
// every call with a store sentinel (never a raw database error).
func testSecretsDeleteRace(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	for round := range 5 {
		name := fmt.Sprintf("d%d", round)
		m, err := s.CreateSecret(ctx, fmt.Sprintf("d-id%d", round), "race", name, "generic", blob("0"), t0)
		if err != nil {
			t.Fatal(err)
		}
		for v := int64(1); v < 12; v++ {
			if _, err := s.AddSecretVersion(ctx, m.ID, v, blob("x"), t0); err != nil {
				t.Fatal(err)
			}
		}
		var wg sync.WaitGroup
		check := func(err error) {
			if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrStale) {
				t.Errorf("unexpected error: %v", err)
			}
		}
		wg.Add(2)
		go func() { defer wg.Done(); _, err := s.AddSecretVersion(ctx, m.ID, 12, blob("y"), t0); check(err) }()
		go func() { defer wg.Done(); check(s.DeleteSecret(ctx, "race", name)) }()
		wg.Wait()
	}
}

// A version CAS names the secret by id: deleting and recreating the name does
// not let a writer that read the old secret add a version to the new one.
func testSecretsRecreate(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	if _, err := s.CreateSecret(ctx, "old", "aba", "x", "password", blob("1"), t0); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSecret(ctx, "aba", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSecret(ctx, "new", "aba", "x", "token", blob("2"), t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSecretVersion(ctx, "old", 1, blob("3"), t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("add by the old id: %v; want ErrNotFound", err)
	}
	if m, _ := s.GetSecretMeta(ctx, "aba", "x"); m.CurrentVersion != 1 || m.Type != "token" {
		t.Fatalf("recreated secret was touched: %+v", m)
	}
}

func testSecretsRewrap(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	mk := func(id, name, kek string) {
		b := blob(id)
		b.KEKID = kek
		if _, err := s.CreateSecret(ctx, id, "rot", name, "generic", b, t0); err != nil {
			t.Fatal(err)
		}
	}
	mk("r1", "a", "old")
	mk("r2", "b", "new")
	mk("r3", "c", "old")

	// Pages in (secret id, version) order and skips rows already under the key.
	page, err := s.ListSecretVersionsNotUnder(ctx, "new", "", 0, 1)
	if err != nil || len(page) != 1 || page[0].SecretID != "r1" || page[0].Scope != "rot" || page[0].Name != "a" || page[0].KEKID != "old" {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	page, err = s.ListSecretVersionsNotUnder(ctx, "new", "r1", 1, 10)
	if err != nil || len(page) != 1 || page[0].SecretID != "r3" {
		t.Fatalf("second page = %+v, %v", page, err)
	}

	// CAS on the old kek_id: the first rewrap wins, a replay and a wrong old id do not.
	ok, err := s.RewrapSecretVersion(ctx, "r1", 1, "old", "new", []byte("rewrapped"))
	if err != nil || !ok {
		t.Fatalf("rewrap = %v, %v", ok, err)
	}
	for _, old := range []string{"old", "other"} {
		if ok, _ := s.RewrapSecretVersion(ctx, "r1", 1, old, "new", []byte("again")); ok {
			t.Fatalf("rewrap with old=%q wrote", old)
		}
	}
	if ok, _ := s.RewrapSecretVersion(ctx, "missing", 1, "old", "new", nil); ok {
		t.Fatal("rewrapped a missing version")
	}
	_, got, err := s.GetSecret(ctx, "rot", "a")
	if err != nil || got.KEKID != "new" || string(got.WrappedDEK) != "rewrapped" || string(got.Ciphertext) != string(blob("r1").Ciphertext) {
		t.Fatalf("after rewrap = %+v, %v", got, err)
	}
	page, _ = s.ListSecretVersionsNotUnder(ctx, "new", "", 0, 10)
	if len(page) != 1 || page[0].SecretID != "r3" {
		t.Fatalf("left to rotate = %+v", page)
	}
}
