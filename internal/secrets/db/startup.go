package db

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/denyszorinets/damson/internal/secrets"
	"github.com/denyszorinets/damson/internal/store"
)

// ErrKEK wraps every startup failure caused by the configured key not fitting
// the database, so a caller can add advice (the dev role names its key file).
var ErrKEK = errors.New("secret encryption key problem")

// CanaryKey is the instance_settings key of the KEK canary.
const CanaryKey = "secrets.kek_canary"

// Settings is the part of the store the startup check needs.
type Settings interface {
	GetSetting(ctx context.Context, key string) (store.Setting, error)
	CreateSetting(ctx context.Context, key, value string) (store.Setting, error)
}

// Startup is the outcome of a successful CheckKEK.
type Startup struct {
	// Enabled reports whether secrets are usable (a KEK is configured).
	Enabled bool
	// RotateDue is set when the canary is still under the previous key.
	RotateDue bool
}

// CheckKEK applies the startup rules: ring is nil when no KEK is configured.
//
//   - no KEK, no canary: secrets disabled
//   - no KEK, canary exists: error, the instance holds data it cannot read
//   - KEK, no canary: store a canary (a peer that wins the race is verified)
//   - KEK matches the canary: start; under the previous key, RotateDue is set
//   - KEK matches neither: error naming kek_ids only
func CheckKEK(ctx context.Context, st Settings, ring *secrets.Keyring, rand io.Reader) (Startup, error) {
	cur, err := st.GetSetting(ctx, CanaryKey)
	missing := errors.Is(err, store.ErrNotFound)
	if err != nil && !missing {
		return Startup{}, fmt.Errorf("reading KEK canary: %w", err)
	}
	if ring == nil {
		if !missing {
			return Startup{}, fmt.Errorf("%w: this instance has encrypted secrets but no KEK is configured (set DAMSON_KEK or secrets.kek_file)", ErrKEK)
		}
		return Startup{}, nil
	}
	if missing {
		raw, err := ring.NewCanary(rand)
		if err != nil {
			return Startup{}, err
		}
		_, err = st.CreateSetting(ctx, CanaryKey, string(raw))
		if err == nil {
			return Startup{Enabled: true}, nil
		}
		if !errors.Is(err, store.ErrConflict) {
			return Startup{}, fmt.Errorf("storing KEK canary: %w", err)
		}
		// A peer controller created it first: verify against its canary.
		if cur, err = st.GetSetting(ctx, CanaryKey); err != nil {
			return Startup{}, fmt.Errorf("reading KEK canary: %w", err)
		}
	}
	switch state, id := ring.CheckCanary([]byte(cur.Value)); state {
	case secrets.CanaryActive:
		return Startup{Enabled: true}, nil
	case secrets.CanaryPrevious:
		return Startup{Enabled: true, RotateDue: true}, nil
	default:
		return Startup{}, fmt.Errorf("%w: the KEK does not match this instance (configured kek_id %s, instance kek_id %s)", ErrKEK, ring.ActiveID(), id)
	}
}
