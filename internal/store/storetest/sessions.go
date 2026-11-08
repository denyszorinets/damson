package storetest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

func newSession(id, hash string) store.NewSession {
	return store.NewSession{
		Session: store.Session{
			ID: id, Sub: "user-1", Email: "a@example.org", Name: "Alice", Claims: []byte(`{"groups":["ops"]}`),
			InstanceAdmin: true, ExpiresAt: t0.Add(12 * time.Hour),
		},
		TokenHash: hash,
	}
}

func testSessions(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	const idle = 2 * time.Hour

	if err := s.CreateSession(ctx, newSession("s1", hashOf("a")), t0); err != nil {
		t.Fatal(err)
	}
	got, err := s.FindSession(ctx, hashOf("a"), t0.Add(time.Minute), idle)
	if err != nil || got.ID != "s1" || got.Sub != "user-1" || !got.InstanceAdmin || string(got.Claims) != `{"groups":["ops"]}` || !got.CreatedAt.Equal(t0) {
		t.Fatalf("FindSession = %+v, %v", got, err)
	}
	if err := s.CreateSession(ctx, newSession("s2", hashOf("a")), t0); !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate token hash: %v; want ErrConflict", err)
	}
	bad := newSession("s3", "short")
	if err := s.CreateSession(ctx, bad, t0); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("short hash: %v; want ErrInvalid", err)
	}
	bad = newSession("s3", hashOf("b"))
	bad.Claims = []byte(`{"x":"` + strings.Repeat("y", store.MaxSessionClaimsBytes) + `"}`)
	if err := s.CreateSession(ctx, bad, t0); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("huge claims: %v; want ErrInvalid", err)
	}
	bad = newSession("s3", hashOf("b"))
	bad.Claims = []byte(`not json`)
	if err := s.CreateSession(ctx, bad, t0); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("bad claims: %v; want ErrInvalid", err)
	}

	// Absolute expiry and idle limit.
	if _, err := s.FindSession(ctx, hashOf("a"), t0.Add(12*time.Hour), time.Hour*100); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expired session: %v; want ErrNotFound", err)
	}
	if _, err := s.FindSession(ctx, hashOf("a"), t0.Add(3*time.Hour), idle); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("idle session: %v; want ErrNotFound", err)
	}
	if _, err := s.FindSession(ctx, hashOf("nope"), t0, idle); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown token: %v; want ErrNotFound", err)
	}

	// Touch keeps a session alive and never moves backwards.
	if err := s.TouchSession(ctx, "s1", t0.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FindSession(ctx, hashOf("a"), t0.Add(3*time.Hour), idle); err != nil {
		t.Errorf("touched session: %v", err)
	}
	if err := s.TouchSession(ctx, "s1", t0); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FindSession(ctx, hashOf("a"), t0.Add(3*time.Hour), idle); !got.LastSeenAt.Equal(t0.Add(90 * time.Minute)) {
		t.Errorf("last_seen_at = %v; touch must not move backwards", got.LastSeenAt)
	}

	// Delete is idempotent and takes effect for every reader.
	if err := s.DeleteSession(ctx, hashOf("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, hashOf("a")); err != nil {
		t.Errorf("second delete: %v", err)
	}
	if _, err := s.FindSession(ctx, hashOf("a"), t0, idle); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleted session: %v; want ErrNotFound", err)
	}
}

func newLogin(state string) store.OIDCLogin {
	return store.OIDCLogin{
		StateHash: shaHex(state), BindingHash: shaHex("bind-" + state), Nonce: "n", Verifier: "v", ReturnTo: "/jobs", ExpiresAt: t0.Add(10 * time.Minute),
	}
}

func testOIDCLogins(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)

	if err := s.CreateOIDCLogin(ctx, newLogin("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOIDCLogin(ctx, newLogin("a")); !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate state: %v; want ErrConflict", err)
	}
	if err := s.CreateOIDCLogin(ctx, store.OIDCLogin{StateHash: "x"}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("incomplete login: %v; want ErrInvalid", err)
	}
	got, err := s.ConsumeOIDCLogin(ctx, shaHex("a"), t0.Add(time.Minute))
	if err != nil || got.Nonce != "n" || got.Verifier != "v" || got.ReturnTo != "/jobs" || got.BindingHash != shaHex("bind-a") || !got.ExpiresAt.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("ConsumeOIDCLogin = %+v, %v", got, err)
	}
	if _, err := s.ConsumeOIDCLogin(ctx, shaHex("a"), t0.Add(time.Minute)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("replayed state: %v; want ErrNotFound", err)
	}

	// An expired login cannot be consumed, and is purged.
	if err := s.CreateOIDCLogin(ctx, newLogin("b")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeOIDCLogin(ctx, shaHex("b"), t0.Add(10*time.Minute)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expired state: %v; want ErrNotFound", err)
	}
	if _, nl, err := s.PurgeExpiredAuth(ctx, t0.Add(time.Hour), time.Hour); err != nil || nl != 1 {
		t.Errorf("PurgeExpiredAuth logins = %d, %v; want 1", nl, err)
	}
}

func testSessionPurge(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	const idle = 2 * time.Hour
	live := newSession("live", shaHex("live"))
	live.ExpiresAt = t0.Add(48 * time.Hour)
	old := newSession("old", shaHex("old"))
	old.ExpiresAt = t0.Add(time.Hour)
	for _, n := range []store.NewSession{live, old} {
		if err := s.CreateSession(ctx, n, t0); err != nil {
			t.Fatal(err)
		}
	}
	// At t0+3h "old" is expired and "live" has been idle for 3h; keep live alive.
	if err := s.TouchSession(ctx, "live", t0.Add(2*time.Hour+30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	ns, _, err := s.PurgeExpiredAuth(ctx, t0.Add(3*time.Hour), idle)
	if err != nil || ns != 1 {
		t.Fatalf("PurgeExpiredAuth sessions = %d, %v; want 1", ns, err)
	}
	if _, err := s.FindSession(ctx, shaHex("live"), t0.Add(3*time.Hour), idle); err != nil {
		t.Errorf("live session purged: %v", err)
	}
}

// A state is consumed by exactly one of many concurrent callbacks.
func testOIDCLoginRace(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	if err := s.CreateOIDCLogin(ctx, newLogin("r")); err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ConsumeOIDCLogin(ctx, shaHex("r"), t0); err == nil {
				wins.Add(1)
			} else if !errors.Is(err, store.ErrNotFound) {
				t.Errorf("consume: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Errorf("%d callbacks consumed one state; want exactly 1", wins.Load())
	}
}

func shaHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
