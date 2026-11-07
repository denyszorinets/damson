package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

func hashOf(c string) string { return strings.Repeat(c, 64) }

func newWebhook(id, jobID, name, hash string) store.NewWebhook {
	return store.NewWebhook{
		Webhook:   store.Webhook{ID: id, JobID: jobID, Name: name, Options: json.RawMessage(`{"env":"dev"}`), OptionsMapping: `{"env": body.env}`},
		TokenHash: hash,
	}
}

func testWebhooks(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	job, _ := mustJob(t, s, "hook-job")

	w, err := s.CreateWebhook(ctx, newWebhook("w1", job.ID, "ci", hashOf("a")), t0)
	if err != nil || w.Version != 1 || w.RevokedAt != nil || w.OptionsMapping == "" {
		t.Fatalf("CreateWebhook = %+v, %v", w, err)
	}
	got, err := s.GetWebhook(ctx, "w1")
	if err != nil || got.Name != "ci" || string(got.Options) == "" || got.LastUsedAt != nil {
		t.Fatalf("GetWebhook = %+v, %v", got, err)
	}
	found, err := s.FindWebhook(ctx, hashOf("a"))
	if err != nil || found.ID != "w1" {
		t.Fatalf("FindWebhook = %+v, %v", found, err)
	}
	if _, err := s.FindWebhook(ctx, hashOf("z")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown token: %v; want ErrNotFound", err)
	}

	// Validation, missing job, duplicate name and token, and a missing actor.
	if _, err := s.CreateWebhook(ctx, newWebhook("w2", job.ID, "x", "short"), t0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("bad hash: %v; want ErrInvalid", err)
	}
	if _, err := s.CreateWebhook(ctx, newWebhook("w2", "no-job", "x", hashOf("b")), t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing job: %v; want ErrNotFound", err)
	}
	if _, err := s.CreateWebhook(ctx, newWebhook("w2", job.ID, "ci", hashOf("b")), t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name: %v; want ErrConflict", err)
	}
	if _, err := s.CreateWebhook(ctx, newWebhook("w2", job.ID, "other", hashOf("a")), t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate token: %v; want ErrConflict", err)
	}
	if _, err := s.CreateWebhook(context.Background(), newWebhook("w2", job.ID, "other", hashOf("b")), t0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("no actor: %v; want ErrInvalid", err)
	}

	// Revoke: the token stops resolving, the row stays, the name can be reused.
	if err := s.RevokeWebhook(ctx, "w1", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeWebhook(ctx, "w1", t0.Add(2*time.Minute)); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if err := s.RevokeWebhook(ctx, "nope", t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke missing: %v; want ErrNotFound", err)
	}
	if err := s.RevokeWebhook(context.Background(), "w1", t0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("revoke without actor: %v; want ErrInvalid", err)
	}
	if _, err := s.FindWebhook(ctx, hashOf("a")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked token still resolves: %v", err)
	}
	if got, _ := s.GetWebhook(ctx, "w1"); got.RevokedAt == nil || !got.RevokedAt.Equal(t0.Add(time.Minute)) || got.Version != 2 {
		t.Fatalf("revoked = %+v", got)
	}
	if _, err := s.CreateWebhook(ctx, newWebhook("w3", job.ID, "ci", hashOf("c")), t0); err != nil {
		t.Fatalf("reusing a revoked name: %v", err)
	}

	list, err := s.ListWebhooks(ctx, job.ID, "", 10)
	if err != nil || len(list) != 2 || list[0].ID != "w1" || list[1].ID != "w3" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if rest, _ := s.ListWebhooks(ctx, "", "w1", 10); len(rest) != 1 || rest[0].ID != "w3" {
		t.Fatalf("after w1 = %+v", rest)
	}

	evs, err := s.ListAuditEvents(ctx, store.AuditFilter{TargetType: store.AuditTargetWebhook}, 50)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]int{}
	for _, e := range evs {
		actions[e.Action]++
		if strings.Contains(string(e.Details), hashOf("a")) || strings.Contains(string(e.Details), "dev") {
			t.Fatalf("audit details leak: %s", e.Details)
		}
	}
	if actions[store.ActionWebhookCreate] != 2 || actions[store.ActionWebhookRevoke] != 1 {
		t.Fatalf("audit actions = %v (a repeated revoke must not add an event)", actions)
	}
}

func testWebhookTouch(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	job, _ := mustJob(t, s, "touch-job")
	if _, err := s.CreateWebhook(ctx, newWebhook("w", job.ID, "ci", hashOf("d")), t0); err != nil {
		t.Fatal(err)
	}
	touch := func(at time.Time) {
		t.Helper()
		if err := s.WithTx(ctx, func(tx store.Tx) error { return tx.TouchWebhook(ctx, "w", at) }); err != nil {
			t.Fatal(err)
		}
	}
	used := func() *time.Time { w, _ := s.GetWebhook(ctx, "w"); return w.LastUsedAt }
	touch(t0)
	if u := used(); u == nil || !u.Equal(t0) {
		t.Fatalf("first touch = %v", u)
	}
	touch(t0.Add(10 * time.Second)) // inside the window: unchanged
	if u := used(); !u.Equal(t0) {
		t.Fatalf("a touch inside the window wrote: %v", u)
	}
	touch(t0.Add(2 * time.Minute))
	if u := used(); !u.Equal(t0.Add(2 * time.Minute)) {
		t.Fatalf("a touch after the window = %v", u)
	}
	if err := s.WithTx(ctx, func(tx store.Tx) error { return tx.TouchWebhook(ctx, "missing", t0) }); err != nil {
		t.Fatalf("touching a missing webhook: %v", err)
	}
}
