package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/secrets"
	secretsdb "github.com/denyszorinets/damson/internal/secrets/db"
	"github.com/denyszorinets/damson/internal/store"
)

// rotateKEK re-wraps every secret version under the active key (DAMSON_KEK or
// secrets.kek_file), reading old ones with the previous key
// (DAMSON_KEK_PREVIOUS or secrets.kek_previous_file). It prints counts and
// kek_ids only, never a key or value.
func rotateKEK(ctx context.Context, st store.Store, c adminCmd, _ time.Time) error {
	if c.secrets.KEK == "" {
		return fmt.Errorf("no key configured: set %s to the new key and %s to the old one", config.EnvKEK, config.EnvKEKPrevious)
	}
	ring, err := secrets.NewKeyring(c.secrets.KEK, c.secrets.KEKPrevious)
	if err != nil {
		return err
	}
	rep, err := secretsdb.RotateKEK(ctx, st, ring, rand.Reader, nil)
	out := map[string]any{
		"active_kek_id": ring.ActiveID(), "rewrapped": rep.Rewrapped, "skipped": rep.Skipped,
		"failed": rep.Failed(), "stragglers": rep.Stragglers, "canary_moved": rep.Canary,
	}
	if c.json {
		if werr := writeJSON(c.stdout, out); werr != nil {
			return errors.Join(err, werr)
		}
	} else {
		fmt.Fprintf(c.stdout, "kek_id %s: %d version(s) re-wrapped, %d skipped, %d failed", ring.ActiveID(), rep.Rewrapped, rep.Skipped, rep.Failed())
		if rep.Canary {
			fmt.Fprint(c.stdout, "; the key canary now uses the new key")
		}
		fmt.Fprintln(c.stdout)
	}
	if err != nil {
		return err
	}
	if rep.Stragglers > 0 {
		fmt.Fprintf(c.stderr, "WARNING: %d version(s) written under the old key during the rotation were re-wrapped. A controller is still running with the old key: restart it with the new key, run rotate-kek again, and keep %s until a run reports no stragglers.\n", rep.Stragglers, config.EnvKEKPrevious)
		return nil
	}
	if rep.Canary {
		fmt.Fprintf(c.stderr, "Rotation finished. Remove %s from every controller's environment and restart them at your pace.\n", config.EnvKEKPrevious)
	}
	return nil
}
