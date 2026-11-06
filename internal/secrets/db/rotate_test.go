package db_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/secrets"
	"github.com/denyszorinets/damson/internal/secrets/db"
	"github.com/denyszorinets/damson/internal/store"
)

func ringOf(t *testing.T, active, previous string) *secrets.Keyring {
	t.Helper()
	r, err := secrets.NewKeyring(active, previous)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func backendOf(s store.Store, ring *secrets.Keyring) *db.Backend {
	var n atomic.Int64
	return db.New(s, ring, rand.Reader, func() time.Time { return now }, func() string { return fmt.Sprintf("rid%d-%d", n.Add(1), time.Now().UnixNano()) })
}

func seed(t *testing.T, s store.Store, ring *secrets.Keyring, n int) {
	t.Helper()
	b := backendOf(s, ring)
	for i := 0; i < n; i++ {
		if _, _, err := b.Put(context.Background(), "proj", fmt.Sprintf("s%d", i), "password", map[string]string{"username": "u", "password": fmt.Sprintf("pw-%d", i)}, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func readAll(t *testing.T, s store.Store, ring *secrets.Keyring, n int) {
	t.Helper()
	b := backendOf(s, ring)
	for i := 0; i < n; i++ {
		got, err := b.Get(context.Background(), "proj", fmt.Sprintf("s%d", i))
		if err != nil || got["password"] != fmt.Sprintf("pw-%d", i) {
			t.Fatalf("secret s%d = %v, %v", i, got, err)
		}
	}
}

func TestRotateKEK(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	oldK, newK := key(t), key(t)
	oldRing := ringOf(t, oldK, "")
	if _, err := db.CheckKEK(ctx, s, oldRing, rand.Reader); err != nil {
		t.Fatal(err)
	}
	seed(t, s, oldRing, 5)

	// With both keys the instance starts, flags rotation as due, and reads everything.
	both := ringOf(t, newK, oldK)
	if st, err := db.CheckKEK(ctx, s, both, rand.Reader); err != nil || !st.RotateDue {
		t.Fatalf("startup = %+v, %v; want RotateDue", st, err)
	}
	readAll(t, s, both, 5)

	// A crash half way: two versions moved by hand, the canary still under the old key.
	left, _ := s.ListSecretVersionsNotUnder(ctx, both.ActiveID(), "", 0, 2)
	for _, r := range left {
		id, w, err := both.Rewrap(rand.Reader, r.Scope, r.Name, r.Version, r.KEKID, r.WrappedDEK)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := s.RewrapSecretVersion(ctx, r.SecretID, r.Version, r.KEKID, id, w); err != nil || !ok {
			t.Fatalf("manual rewrap = %v, %v", ok, err)
		}
	}
	readAll(t, s, both, 5) // mixed kek_ids still decrypt
	if st, _ := db.CheckKEK(ctx, s, both, rand.Reader); !st.RotateDue {
		t.Fatal("the canary moved before the rotation finished")
	}

	rep, err := db.RotateKEK(ctx, s, both, rand.Reader, nil)
	if err != nil || rep.Rewrapped != 3 || !rep.Canary || rep.Failed() != 0 {
		t.Fatalf("rotation = %+v, %v; want 3 rewrapped and the canary moved", rep, err)
	}
	// After rotation the new key alone is enough.
	only := ringOf(t, newK, "")
	if st, err := db.CheckKEK(ctx, s, only, rand.Reader); err != nil || st.RotateDue {
		t.Fatalf("startup with the new key = %+v, %v", st, err)
	}
	readAll(t, s, only, 5)

	// Running it again changes nothing.
	rep, err = db.RotateKEK(ctx, s, both, rand.Reader, nil)
	if err != nil || rep.Rewrapped != 0 || rep.Canary {
		t.Fatalf("second rotation = %+v, %v; want a no-op", rep, err)
	}
}

func TestRotateKEKRefusals(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	oldK, newK, thirdK := key(t), key(t), key(t)
	if _, err := db.RotateKEK(ctx, s, ringOf(t, newK, oldK), rand.Reader, nil); !errors.Is(err, db.ErrKEK) {
		t.Fatalf("rotation of an instance without secrets = %v", err)
	}
	oldRing := ringOf(t, oldK, "")
	if _, err := db.CheckKEK(ctx, s, oldRing, rand.Reader); err != nil {
		t.Fatal(err)
	}
	seed(t, s, oldRing, 2)
	// Keys that do not match the instance.
	if _, err := db.RotateKEK(ctx, s, ringOf(t, newK, thirdK), rand.Reader, nil); !errors.Is(err, db.ErrKEK) {
		t.Fatalf("rotation with unrelated keys = %v", err)
	}
	// A version under a third key blocks the canary and is counted.
	third := ringOf(t, thirdK, "")
	b := backendOf(s, third)
	if _, _, err := b.Put(ctx, "proj", "odd", "generic", map[string]string{"v": "x"}, nil); err != nil {
		t.Fatal(err)
	}
	rep, err := db.RotateKEK(ctx, s, ringOf(t, newK, oldK), rand.Reader, nil)
	if !errors.Is(err, db.ErrKEK) || rep.Failed() != 1 || rep.Rewrapped != 2 || rep.Canary {
		t.Fatalf("rotation = %+v, %v; want 2 rewrapped, 1 failed, canary kept", rep, err)
	}
	if st, _ := db.CheckKEK(ctx, s, ringOf(t, newK, oldK), rand.Reader); !st.RotateDue {
		t.Fatal("the canary moved although a version could not be rotated")
	}
}

// A store that lets a controller still on the old key write a version behind
// the scan cursor in the middle of the first pass.
type racingStore struct {
	store.Store
	inject func()
	once   bool
}

func (r *racingStore) ListSecretVersionsNotUnder(ctx context.Context, kek, after string, v int64, limit int) ([]store.WrappedVersion, error) {
	rows, err := r.Store.ListSecretVersionsNotUnder(ctx, kek, after, v, limit)
	if !r.once {
		r.once = true
		r.inject()
	}
	return rows, err
}

func TestRotateKEKCatchesLateWrites(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	oldK, newK := key(t), key(t)
	oldRing := ringOf(t, oldK, "")
	if _, err := db.CheckKEK(ctx, s, oldRing, rand.Reader); err != nil {
		t.Fatal(err)
	}
	seed(t, s, oldRing, 3)
	rs := &racingStore{Store: s}
	rs.inject = func() {
		b := backendOf(s, oldRing) // a controller that never got the new key
		if _, _, err := b.Put(ctx, "proj", "late", "generic", map[string]string{"v": "x"}, nil); err != nil {
			t.Error(err)
		}
	}
	rep, err := db.RotateKEK(ctx, rs, ringOf(t, newK, oldK), rand.Reader, nil)
	if err != nil || rep.Rewrapped != 4 || !rep.Canary {
		t.Fatalf("rotation = %+v, %v; want the late version rewrapped too", rep, err)
	}
	if left, _ := s.ListSecretVersionsNotUnder(ctx, ringOf(t, newK, "").ActiveID(), "", 0, 10); len(left) != 0 {
		t.Fatalf("%d version(s) left under the old key", len(left))
	}
	// A write after the canary moved is found by the closing scan.
	b := backendOf(s, oldRing)
	if _, _, err := b.Put(ctx, "proj", "later", "generic", map[string]string{"v": "y"}, nil); err != nil {
		t.Fatal(err)
	}
	rep, err = db.RotateKEK(ctx, s, ringOf(t, newK, oldK), rand.Reader, nil)
	if err != nil || rep.Stragglers != 1 {
		t.Fatalf("second rotation = %+v, %v; want one straggler", rep, err)
	}
}

// Losing the canary CAS is fine only to a canary under our active key.
type staleCanary struct {
	store.Store
	writeKey *secrets.Keyring
}

func (c *staleCanary) UpdateSetting(ctx context.Context, key, value string, expected int64) (store.Setting, error) {
	raw, _ := c.writeKey.NewCanary(rand.Reader)
	if _, err := c.Store.UpdateSetting(ctx, key, string(raw), expected); err != nil {
		return store.Setting{}, err
	}
	return store.Setting{}, store.ErrStale
}

func TestRotateKEKCanaryRace(t *testing.T) {
	for name, tc := range map[string]struct{ sameKey, wantErr bool }{"peer used our key": {true, false}, "something else rewrote it": {false, true}} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := newStore(t)
			oldK, newK := key(t), key(t)
			oldRing := ringOf(t, oldK, "")
			if _, err := db.CheckKEK(ctx, s, oldRing, rand.Reader); err != nil {
				t.Fatal(err)
			}
			seed(t, s, oldRing, 1)
			ring := ringOf(t, newK, oldK)
			writer := ring
			if !tc.sameKey {
				writer = ringOf(t, key(t), "")
			}
			_, err := db.RotateKEK(ctx, &staleCanary{Store: s, writeKey: writer}, ring, rand.Reader, nil)
			if (err != nil) != tc.wantErr || (err != nil && !errors.Is(err, db.ErrKEK)) {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
