package storetest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

func newToken(id, account, name, hash string) store.NewServiceToken {
	return store.NewServiceToken{
		ServiceToken: store.ServiceToken{ID: id, ServiceAccountID: account, Name: name, Project: "default", Role: store.RoleRunner},
		TokenHash:    hash,
	}
}

func testServiceAccounts(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)

	a, err := s.CreateServiceAccount(ctx, store.ServiceAccount{ID: "sa1", Name: "ci", Description: "builds"}, t0)
	if err != nil || a.Name != "ci" {
		t.Fatalf("CreateServiceAccount = %+v, %v", a, err)
	}
	if _, err := s.CreateServiceAccount(ctx, store.ServiceAccount{ID: "sa2", Name: "ci"}, t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name: %v; want ErrConflict", err)
	}
	if _, err := s.CreateServiceAccount(context.Background(), store.ServiceAccount{ID: "sa3", Name: "x"}, t0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("no actor: %v; want ErrInvalid", err)
	}
	if got, err := s.GetServiceAccount(ctx, "sa1"); err != nil || got.Description != "builds" {
		t.Fatalf("GetServiceAccount = %+v, %v", got, err)
	}
	if _, err := s.GetServiceAccount(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := s.CreateServiceAccount(ctx, store.ServiceAccount{ID: "sa4", Name: "deploy"}, t0); err != nil {
		t.Fatal(err)
	}
	if page, _ := s.ListServiceAccounts(ctx, "sa1", 10); len(page) != 1 || page[0].ID != "sa4" {
		t.Fatalf("after sa1 = %+v", page)
	}

	// Tokens: validation, lookup by hash, expiry, revoke.
	bad := newToken("t0", "sa1", "x", hashOf("1"))
	bad.Role = "admin"
	if _, err := s.CreateServiceToken(ctx, bad, t0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("admin role: %v; want ErrInvalid", err)
	}
	if _, err := s.CreateServiceToken(ctx, newToken("t0", "nope", "x", hashOf("1")), t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing account: %v; want ErrNotFound", err)
	}
	exp := t0.Add(time.Hour)
	withExp := newToken("t1", "sa1", "short", hashOf("a"))
	withExp.ExpiresAt = &exp
	if _, err := s.CreateServiceToken(ctx, withExp, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateServiceToken(ctx, newToken("t2", "sa1", "forever", hashOf("b")), t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateServiceToken(ctx, newToken("t3", "sa1", "dup", hashOf("b")), t0); err == nil {
		t.Fatal("a duplicate token hash was accepted")
	}
	found, err := s.FindServiceToken(ctx, hashOf("a"), t0.Add(time.Minute))
	if err != nil || found.ID != "t1" || found.Role != store.RoleRunner || found.Project != "default" {
		t.Fatalf("FindServiceToken = %+v, %v", found, err)
	}
	if _, err := s.FindServiceToken(ctx, hashOf("a"), exp); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired token: %v; want ErrNotFound (expires_at is exclusive)", err)
	}
	if _, err := s.FindServiceToken(ctx, hashOf("z"), t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown token: %v", err)
	}
	if err := s.RevokeServiceToken(ctx, "t2", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeServiceToken(ctx, "t2", t0); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if err := s.RevokeServiceToken(ctx, "nope", t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke missing: %v", err)
	}
	if err := s.RevokeServiceToken(context.Background(), "t1", t0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("revoke without actor: %v", err)
	}
	if _, err := s.FindServiceToken(ctx, hashOf("b"), t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked token resolves: %v", err)
	}
	list, err := s.ListServiceTokens(ctx, "sa1")
	if err != nil || len(list) != 2 || list[1].RevokedAt == nil || list[0].ExpiresAt == nil {
		t.Fatalf("ListServiceTokens = %+v, %v", list, err)
	}

	// last_used_at is throttled and best effort.
	if err := s.TouchServiceToken(ctx, "t1", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchServiceToken(ctx, "t1", t0.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetServiceToken(ctx, "t1"); got.LastUsedAt == nil || !got.LastUsedAt.Equal(t0) {
		t.Fatalf("last_used_at = %v; want %v (second touch inside the window)", got.LastUsedAt, t0)
	}
	if err := s.TouchServiceToken(ctx, "missing", t0); err != nil {
		t.Fatalf("touching a missing token: %v", err)
	}

	// Deleting the account takes its tokens along.
	if err := s.DeleteServiceAccount(ctx, "sa1", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteServiceAccount(ctx, "sa1", t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if err := s.DeleteServiceAccount(context.Background(), "sa4", t0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("delete without actor: %v", err)
	}
	if _, err := s.GetServiceToken(ctx, "t1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("token survived its account: %v", err)
	}

	// Audit: no hash, no secret.
	var n int
	for _, tt := range []string{store.AuditTargetServiceAccount, store.AuditTargetServiceToken} {
		evs, err := s.ListAuditEvents(ctx, store.AuditFilter{TargetType: tt}, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			n++
			if strings.Contains(string(e.Details), strings.Repeat("a", 64)) {
				t.Fatalf("audit details leak a hash: %s", e.Details)
			}
		}
	}
	if n != 6 { // accounts: 2 create + 1 delete; tokens: 2 create + 1 revoke (a repeated revoke adds none)
		t.Fatalf("%d audit events", n)
	}
}
