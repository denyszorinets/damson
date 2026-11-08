// Package secretstest is the contract every secret backend must pass: the db
// backend and the openbao backend run the same cases.
package secretstest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/secrets"
	"github.com/denyszorinets/damson/internal/store"
)

// Backend is the method set shared by the secret backends.
type Backend interface {
	Put(ctx context.Context, scope, name, typ string, fields map[string]string, ifVersion *int64) (store.SecretMeta, bool, error)
	Get(ctx context.Context, scope, name string) (map[string]string, error)
	Meta(ctx context.Context, scope, name string) (store.SecretMeta, error)
	List(ctx context.Context, scope, afterScope, afterName string, limit int) ([]store.SecretMeta, error)
	Delete(ctx context.Context, scope, name string) error
}

// Fixture is a fresh backend and the audit trail its writes land in.
type Fixture struct {
	Backend Backend
	Audit   store.AuditStore
}

func i64(n int64) *int64 { return &n }

// Ctx returns a context carrying the actor the audited writes need.
func Ctx() context.Context {
	return store.WithAudit(context.Background(), store.AuditMeta{Actor: "test:actor", RequestID: "req-1"})
}

// Run runs the contract; mk makes a fresh Fixture for every case.
func Run(t *testing.T, mk func(t *testing.T) Fixture) {
	cases := []struct {
		name string
		fn   func(t *testing.T, f Fixture)
	}{
		{"CreateGetMeta", createGetMeta},
		{"Versions", versions},
		{"Conditional", conditional},
		{"TypeChange", typeChange},
		{"Delete", deleteCase},
		{"Validation", validation},
		{"ListAndPaging", listAndPaging},
		{"Inventory", inventory},
		{"Audit", audit},
		{"NoActorNoWrite", noActor},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.fn(t, mk(t)) })
	}
}

var pw = map[string]string{"username": "root", "password": "hunter2-value"}

func createGetMeta(t *testing.T, f Fixture) {
	ctx := Ctx()
	m, created, err := f.Backend.Put(ctx, "proj", "db", "password", pw, nil)
	if err != nil || !created || m.CurrentVersion != 1 || m.Type != "password" || strings.Join(m.FieldNames, ",") != "password,username" {
		t.Fatalf("create = %+v %v %v", m, created, err)
	}
	got, err := f.Backend.Get(ctx, "proj", "db")
	if err != nil || got["password"] != "hunter2-value" || len(got) != 2 {
		t.Fatalf("get = %v %v", got, err)
	}
	meta, err := f.Backend.Meta(ctx, "proj", "db")
	if err != nil || meta.CurrentVersion != 1 || meta.Scope != "proj" || meta.Name != "db" || meta.CreatedAt.IsZero() {
		t.Fatalf("meta = %+v %v", meta, err)
	}
	if _, err := f.Backend.Get(ctx, "proj", "nope"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("get missing = %v", err)
	}
	if _, err := f.Backend.Meta(ctx, "proj", "nope"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("meta missing = %v", err)
	}
}

func versions(t *testing.T, f Fixture) {
	ctx := Ctx()
	if _, _, err := f.Backend.Put(ctx, "proj", "tok", "token", map[string]string{"token": "one"}, nil); err != nil {
		t.Fatal(err)
	}
	m, created, err := f.Backend.Put(ctx, "proj", "tok", "token", map[string]string{"token": "two"}, nil)
	if err != nil || created || m.CurrentVersion != 2 {
		t.Fatalf("second = %+v %v %v", m, created, err)
	}
	got, _ := f.Backend.Get(ctx, "proj", "tok")
	if got["token"] != "two" {
		t.Fatalf("current version = %v", got)
	}
}

func conditional(t *testing.T, f Fixture) {
	ctx := Ctx()
	one := map[string]string{"token": "one"}
	if _, _, err := f.Backend.Put(ctx, "proj", "tok", "token", one, i64(3)); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("if-version on a missing secret = %v; want not found", err)
	}
	if _, created, err := f.Backend.Put(ctx, "proj", "tok", "token", one, i64(0)); err != nil || !created {
		t.Fatalf("create-only = %v %v", created, err)
	}
	if _, _, err := f.Backend.Put(ctx, "proj", "tok", "token", one, i64(0)); !errors.Is(err, secrets.ErrVersionConflict) {
		t.Fatalf("create-only again = %v; want conflict", err)
	}
	if _, _, err := f.Backend.Put(ctx, "proj", "tok", "token", one, i64(5)); !errors.Is(err, secrets.ErrVersionConflict) {
		t.Fatalf("wrong version = %v; want conflict", err)
	}
	if m, _, err := f.Backend.Put(ctx, "proj", "tok", "token", map[string]string{"token": "two"}, i64(1)); err != nil || m.CurrentVersion != 2 {
		t.Fatalf("right version = %+v %v", m, err)
	}
}

func typeChange(t *testing.T, f Fixture) {
	ctx := Ctx()
	if _, _, err := f.Backend.Put(ctx, "proj", "x", "token", map[string]string{"token": "t"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Backend.Put(ctx, "proj", "x", "password", pw, nil); !errors.Is(err, secrets.ErrTypeChange) {
		t.Fatalf("type change = %v", err)
	}
}

func deleteCase(t *testing.T, f Fixture) {
	ctx := Ctx()
	if err := f.Backend.Delete(ctx, "proj", "nope"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("delete missing = %v", err)
	}
	if _, _, err := f.Backend.Put(ctx, "proj", "tok", "token", map[string]string{"token": "t"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Backend.Delete(ctx, "proj", "tok"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Backend.Get(ctx, "proj", "tok"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
	if _, created, err := f.Backend.Put(ctx, "proj", "tok", "token", map[string]string{"token": "again"}, i64(0)); err != nil || !created {
		t.Fatalf("recreate = %v %v", created, err)
	}
}

func validation(t *testing.T, f Fixture) {
	ctx := Ctx()
	tests := []struct {
		name           string
		scope, nm, typ string
		fields         map[string]string
		wantErr        error
	}{
		{"bad scope", "Bad Scope", "x", "token", map[string]string{"token": "t"}, secrets.ErrBadRef},
		{"bad name", "proj", "a/b", "token", map[string]string{"token": "t"}, secrets.ErrBadRef},
		{"unknown type", "proj", "x", "nope", map[string]string{"token": "t"}, secrets.ErrBadType},
		{"missing required field", "proj", "x", "password", map[string]string{"username": "u"}, secrets.ErrBadFields},
		{"unknown field", "proj", "x", "token", map[string]string{"token": "t", "extra": "e"}, secrets.ErrBadFields},
		{"no fields", "proj", "x", "generic", map[string]string{}, secrets.ErrBadFields},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := f.Backend.Put(ctx, tc.scope, tc.nm, tc.typ, tc.fields, nil); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v; want %v", err, tc.wantErr)
			}
		})
	}
}

func listAndPaging(t *testing.T, f Fixture) {
	ctx := Ctx()
	tok := map[string]string{"token": "t"}
	for _, s := range []struct{ scope, name string }{
		{"a", "one"}, {"a", "two"}, {"a/b", "deep"}, {"a-b", "dash"}, {"c", "three"},
	} {
		if _, _, err := f.Backend.Put(ctx, s.scope, s.name, "token", tok, nil); err != nil {
			t.Fatal(err)
		}
	}
	all, err := f.Backend.List(ctx, "", "", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range all {
		got = append(got, m.Scope+"/"+m.Name)
	}
	want := []string{"a/one", "a/two", "a-b/dash", "a/b/deep", "c/three"}
	// The order is by (scope, name) as strings: a, then a-b, then a/b.
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("list = %v; want %v", got, want)
	}
	only, err := f.Backend.List(ctx, "a", "", "", 100)
	if err != nil || len(only) != 2 || only[0].Name != "one" || only[1].Name != "two" {
		t.Fatalf("scope a = %+v %v", only, err)
	}
	// Page through two at a time.
	var paged []string
	afterScope, afterName := "", ""
	for range 10 {
		page, err := f.Backend.List(ctx, "", afterScope, afterName, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, m := range page {
			paged = append(paged, m.Scope+"/"+m.Name)
		}
		last := page[len(page)-1]
		afterScope, afterName = last.Scope, last.Name
	}
	if strings.Join(paged, ",") != strings.Join(want, ",") {
		t.Fatalf("paged = %v; want %v", paged, want)
	}
	for _, m := range all {
		if m.Type != "token" || len(m.FieldNames) != 1 || m.FieldNames[0] != "token" {
			t.Fatalf("list metadata = %+v", m)
		}
	}
}

func inventory(t *testing.T, f Fixture) {
	ctx := Ctx()
	if _, _, err := f.Backend.Put(ctx, secrets.InventoryScope, "web-1", "password", pw, nil); err != nil {
		t.Fatal(err)
	}
	got, err := f.Backend.Get(ctx, secrets.InventoryScope, "web-1")
	if err != nil || got["username"] != "root" {
		t.Fatalf("get = %v %v", got, err)
	}
	inv, err := f.Backend.List(ctx, secrets.InventoryScope, "", "", 10)
	if err != nil || len(inv) != 1 {
		t.Fatalf("inventory list = %+v %v", inv, err)
	}
}

func audit(t *testing.T, f Fixture) {
	ctx := Ctx()
	if _, _, err := f.Backend.Put(ctx, "proj", "tok", "token", map[string]string{"token": "super-secret-value"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Backend.Put(ctx, "proj", "tok", "token", map[string]string{"token": "another-secret-value"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Backend.Delete(ctx, "proj", "tok"); err != nil {
		t.Fatal(err)
	}
	evs, err := f.Audit.ListAuditEvents(ctx, store.AuditFilter{TargetType: store.AuditTargetSecret, Target: "proj/tok"}, 10)
	if err != nil || len(evs) != 3 {
		t.Fatalf("events = %d %v", len(evs), err)
	}
	actions := []string{evs[2].Action, evs[1].Action, evs[0].Action} // newest first
	if strings.Join(actions, ",") != "secret.create,secret.update,secret.delete" {
		t.Fatalf("actions = %v", actions)
	}
	for _, ev := range evs {
		if ev.Actor != "test:actor" || ev.ProjectPath != "proj" {
			t.Fatalf("event = %+v", ev)
		}
		if strings.Contains(string(ev.Details), "secret-value") {
			t.Fatalf("a value reached the audit trail: %s", ev.Details)
		}
	}
	if !strings.Contains(string(evs[1].Details), `"version":2`) {
		t.Fatalf("update details = %s", evs[1].Details)
	}
}

func noActor(t *testing.T, f Fixture) {
	bare := context.Background()
	if _, _, err := f.Backend.Put(bare, "proj", "x", "token", map[string]string{"token": "t"}, nil); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("put without an actor = %v; want ErrInvalid", err)
	}
	if _, err := f.Backend.Get(bare, "proj", "x"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("a refused put left a secret: %v", err)
	}
	if _, _, err := f.Backend.Put(Ctx(), "proj", "x", "token", map[string]string{"token": "t"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Backend.Delete(bare, "proj", "x"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("delete without an actor = %v; want ErrInvalid", err)
	}
	if _, err := f.Backend.Get(bare, "proj", "x"); err != nil {
		t.Fatalf("a refused delete removed the secret: %v", err)
	}
}
