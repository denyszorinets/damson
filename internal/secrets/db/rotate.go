package db

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/denyszorinets/damson/internal/secrets"
	"github.com/denyszorinets/damson/internal/store"
)

// RotateStore is the part of the store rotate-kek needs.
type RotateStore interface {
	store.SecretStore
	GetSetting(ctx context.Context, key string) (store.Setting, error)
	UpdateSetting(ctx context.Context, key, value string, expectedVersion int64) (store.Setting, error)
}

// RotateReport counts what a rotation did.
type RotateReport struct {
	Rewrapped     int // versions moved to the active key
	Skipped       int // versions another process rotated or deleted meanwhile
	UnknownKEK    int // versions wrapped under a key this keyring does not hold
	Undecryptable int // versions whose wrapped DEK does not open under the key it names
	Canary        bool
	// Stragglers counts versions found under another key after the canary
	// moved: a controller is still writing with the old key, so the previous
	// key must be kept.
	Stragglers int
}

// Failed is the number of versions that could not be rotated.
func (r RotateReport) Failed() int { return r.UnknownKEK + r.Undecryptable }

const rotateBatch = 100

// RotateKEK re-wraps every secret version still wrapped under the previous key
// with the active one, then the canary, last. It is safe to run while
// controllers run (each holds both keys until the canary moves) and to resume:
// every row is a compare-and-swap on its old kek_id, so a crash leaves mixed
// kek_ids that all still decrypt, and a rerun finds only what is left. It
// repeats full passes until one finds nothing to rewrap (a controller still on
// the old key may write behind the cursor), moves the canary only when no
// version failed, and scans once more afterwards to count stragglers.
func RotateKEK(ctx context.Context, st RotateStore, ring *secrets.Keyring, rand io.Reader, progress func(RotateReport)) (RotateReport, error) {
	var rep RotateReport
	cur, err := st.GetSetting(ctx, CanaryKey)
	if errors.Is(err, store.ErrNotFound) {
		return rep, fmt.Errorf("%w: this instance has no encrypted secrets yet (no KEK canary)", ErrKEK)
	}
	if err != nil {
		return rep, fmt.Errorf("reading KEK canary: %w", err)
	}
	state, id := ring.CheckCanary([]byte(cur.Value))
	if state == secrets.CanaryMismatch {
		return rep, fmt.Errorf("%w: the configured keys do not match this instance (configured kek_id %s, previous %s, instance kek_id %s); a canary under the old key needs it as the previous key", ErrKEK, ring.ActiveID(), orNone(ring.PreviousID()), id)
	}
	for {
		moved, err := rotatePass(ctx, st, ring, rand, &rep, progress)
		if err != nil {
			return rep, err
		}
		if moved == 0 {
			break
		}
	}
	if rep.Failed() > 0 {
		return rep, fmt.Errorf("%w: %d secret version(s) are wrapped under a key that is neither the active (%s) nor the previous (%s) key and %d cannot be opened under the key they name; the canary was not moved", ErrKEK, rep.UnknownKEK, ring.ActiveID(), orNone(ring.PreviousID()), rep.Undecryptable)
	}
	if state == secrets.CanaryPrevious {
		if err := moveCanary(ctx, st, ring, rand, cur.Version); err != nil {
			return rep, err
		}
		rep.Canary = true
	}
	// A controller that was still on the old key may have written since.
	before := rep.Rewrapped
	if _, err := rotatePass(ctx, st, ring, rand, &rep, progress); err != nil {
		return rep, err
	}
	rep.Stragglers = rep.Rewrapped - before
	if state == secrets.CanaryActive {
		rep.Stragglers = rep.Rewrapped // the canary moved in an earlier run: nothing is left over from it
	}
	return rep, nil
}

// moveCanary stores a canary under the active key. Losing the CAS is fine only
// if the winner stored one under our active key.
func moveCanary(ctx context.Context, st RotateStore, ring *secrets.Keyring, rand io.Reader, version int64) error {
	raw, err := ring.NewCanary(rand)
	if err != nil {
		return err
	}
	_, err = st.UpdateSetting(ctx, CanaryKey, string(raw), version)
	if err == nil {
		return nil
	}
	if !errors.Is(err, store.ErrStale) {
		return fmt.Errorf("storing the rotated KEK canary: %w", err)
	}
	now, gerr := st.GetSetting(ctx, CanaryKey)
	if gerr != nil {
		return fmt.Errorf("reading KEK canary: %w", gerr)
	}
	if state, id := ring.CheckCanary([]byte(now.Value)); state != secrets.CanaryActive {
		return fmt.Errorf("%w: the KEK canary changed during rotation and is not under the active key %s (instance kek_id %s)", ErrKEK, ring.ActiveID(), id)
	}
	return nil
}

// rotatePass scans all versions not under the active key once and rewraps what
// it can; it returns how many it rewrapped.
func rotatePass(ctx context.Context, st RotateStore, ring *secrets.Keyring, rand io.Reader, rep *RotateReport, progress func(RotateReport)) (int, error) {
	moved := 0
	rep.UnknownKEK, rep.Undecryptable = 0, 0 // failures describe the latest pass
	afterID, afterVer := "", int64(0)
	for {
		rows, err := st.ListSecretVersionsNotUnder(ctx, ring.ActiveID(), afterID, afterVer, rotateBatch)
		if err != nil {
			return moved, err
		}
		for _, r := range rows {
			afterID, afterVer = r.SecretID, r.Version
			newID, wrapped, err := ring.Rewrap(rand, r.Scope, r.Name, r.Version, r.KEKID, r.WrappedDEK)
			switch {
			case errors.Is(err, secrets.ErrUnknownKEK):
				rep.UnknownKEK++
				continue
			case errors.Is(err, secrets.ErrDecrypt):
				rep.Undecryptable++
				continue
			case err != nil:
				return moved, err
			}
			ok, err := st.RewrapSecretVersion(ctx, r.SecretID, r.Version, r.KEKID, newID, wrapped)
			if err != nil {
				return moved, err
			}
			if ok {
				rep.Rewrapped++
				moved++
			} else {
				rep.Skipped++
			}
		}
		if progress != nil {
			progress(*rep)
		}
		if len(rows) < rotateBatch {
			break
		}
	}
	return moved, nil
}

func orNone(id string) string {
	if id == "" {
		return "none"
	}
	return id
}
