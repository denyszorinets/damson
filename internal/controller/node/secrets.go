package node

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/secrets"
	secretsdb "github.com/denyszorinets/damson/internal/secrets/db"
)

// startSecrets applies the KEK startup rules and builds the db backend. It
// fails the start when the configured key does not match the database.
func (n *Node) startSecrets(ctx context.Context, cfg Config) error {
	var ring *secrets.Keyring
	if cfg.KEK != "" {
		var err error
		if ring, err = secrets.NewKeyring(cfg.KEK, cfg.KEKPrevious); err != nil {
			return fmt.Errorf("configuring secrets: %w", err)
		}
	}
	st, err := secretsdb.CheckKEK(ctx, cfg.Store, ring, rand.Reader)
	if err != nil {
		return fmt.Errorf("checking the KEK: %w", err)
	}
	log := cfg.Logger
	switch {
	case !st.Enabled:
		log.Warn("secrets are disabled: no KEK configured (set DAMSON_KEK or secrets.kek_file)")
		return nil
	case st.RotateDue:
		log.Warn("the instance is still encrypted under the previous KEK; run `damson admin rotate-kek`", "kek_id", ring.ActiveID(), "previous_kek_id", ring.PreviousID())
	default:
		log.Info("secrets enabled", "kek_id", ring.ActiveID())
	}
	n.secrets = secretsdb.New(cfg.Store, ring, rand.Reader, time.Now, uuid.NewString)
	return nil
}

// secretSource adapts the db backend to the dispatcher. Failures that no retry
// can fix are mapped to the permanent dispatch errors; the controller logs the
// ref and key id, never a value.
type secretSource struct {
	b   *secretsdb.Backend
	log *slog.Logger
}

// NewSecretSource adapts a db backend to the dispatcher.
func NewSecretSource(b *secretsdb.Backend, log *slog.Logger) dispatch.SecretSource {
	return &secretSource{b: b, log: log}
}

func (s *secretSource) Secret(ctx context.Context, _, ref string) (map[string]string, error) {
	r, err := secrets.ParseRef(ref)
	if err != nil {
		s.log.Error("secret ref is malformed", "ref", ref)
		return nil, dispatch.ErrSecretUnavailable
	}
	fields, err := s.b.Get(ctx, r.Scope, r.Name)
	switch {
	case err == nil:
		return fields, nil
	case errors.Is(err, secrets.ErrNotFound):
		return nil, dispatch.ErrUnknownSecret
	case errors.Is(err, secrets.ErrUnknownKEK), errors.Is(err, secrets.ErrDecrypt):
		s.log.Error("secret cannot be decrypted", "ref", r.Base(), "error", err.Error())
		return nil, dispatch.ErrSecretUnavailable
	}
	return nil, err
}
