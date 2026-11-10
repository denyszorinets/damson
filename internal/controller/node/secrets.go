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
	"github.com/denyszorinets/damson/internal/devnodes"
	"github.com/denyszorinets/damson/internal/secrets"
	secretsdb "github.com/denyszorinets/damson/internal/secrets/db"
	"github.com/denyszorinets/damson/internal/secrets/openbao"
	"github.com/denyszorinets/damson/internal/store"
)

// secretBackend is what the node needs from a secret backend: the db backend
// and the openbao backend both provide it.
type secretBackend interface {
	Put(ctx context.Context, scope, name, typ string, fields map[string]string, ifVersion *int64) (store.SecretMeta, bool, error)
	Get(ctx context.Context, scope, name string) (map[string]string, error)
	Meta(ctx context.Context, scope, name string) (store.SecretMeta, error)
	List(ctx context.Context, scope, afterScope, afterName string, limit int) ([]store.SecretMeta, error)
	Delete(ctx context.Context, scope, name string) error
}

// dispatchGetter is implemented by a backend that has a cache for the dispatch
// path.
type dispatchGetter interface {
	GetForDispatch(ctx context.Context, scope, name string) (map[string]string, error)
}

// startOpenBao builds the openbao backend and logs in. The db KEK rules do not
// apply: a canary left by an earlier db setup only draws a warning.
func (n *Node) startOpenBao(ctx context.Context, cfg Config) error {
	log := cfg.Logger
	c := *cfg.OpenBao
	c.Logger = log
	cl, err := openbao.New(c)
	if err != nil {
		return fmt.Errorf("configuring the openbao secret backend: %w", err)
	}
	b := openbao.NewBackend(cl, cfg.Store, time.Now)
	if err := b.Start(ctx); err != nil {
		return fmt.Errorf("starting the openbao secret backend: %w", err)
	}
	if _, err := cfg.Store.GetSetting(ctx, secretsdb.CanaryKey); err == nil {
		log.Warn("the database holds secrets from the db backend; they are not served while secrets.backend is openbao")
	}
	n.secrets, n.secretsStop = b, b.Stop
	log.Info("secrets enabled", "backend", "openbao", "mount", c.Mount, "prefix", c.Prefix, "read_only", c.ReadOnly)
	return nil
}

// startSecrets applies the KEK startup rules and builds the db backend. It
// fails the start when the configured key does not match the database.
func (n *Node) startSecrets(ctx context.Context, cfg Config) error {
	if cfg.OpenBao != nil {
		return n.startOpenBao(ctx, cfg)
	}
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
	b   secretBackend
	log *slog.Logger
}

// NewSecretSource adapts a db backend to the dispatcher.
func NewSecretSource(b secretBackend, log *slog.Logger) dispatch.SecretSource {
	return &secretSource{b: b, log: log}
}

func (s *secretSource) Secret(ctx context.Context, _, ref string) (map[string]string, error) {
	r, err := secrets.ParseRef(ref)
	if err != nil {
		s.log.Error("secret ref is malformed", "ref", ref)
		return nil, dispatch.ErrSecretUnavailable
	}
	var fields map[string]string
	if g, ok := s.b.(dispatchGetter); ok {
		fields, err = g.GetForDispatch(ctx, r.Scope, r.Name)
	} else {
		fields, err = s.b.Get(ctx, r.Scope, r.Name)
	}
	switch {
	case err == nil:
		return fields, nil
	case errors.Is(err, secrets.ErrNotFound):
		return nil, dispatch.ErrUnknownSecret
	case errors.Is(err, secrets.ErrUnavailable):
		s.log.Error("secret is unavailable", "ref", r.Base(), "error", err.Error())
		return nil, dispatch.ErrSecretUnavailable
	case errors.Is(err, secrets.ErrUnknownKEK), errors.Is(err, secrets.ErrDecrypt):
		s.log.Error("secret cannot be decrypted", "ref", r.Base(), "error", err.Error())
		return nil, dispatch.ErrSecretUnavailable
	}
	return nil, err
}

// projectOf resolves the project path of a run: run, job, project.
func projectOf(st store.Store) func(ctx context.Context, runID string) (string, error) {
	return func(ctx context.Context, runID string) (string, error) {
		run, err := st.GetRun(ctx, runID)
		if err != nil {
			return "", err
		}
		job, err := st.GetJob(ctx, run.JobID)
		if err != nil {
			return "", err
		}
		p, err := st.GetProject(ctx, job.ProjectID)
		if err != nil {
			return "", fmt.Errorf("project of job %q: %w", job.ID, err)
		}
		return p.Path, nil
	}
}

// seedDevFile writes the --dev-nodes file into the inventory and the secret
// store (ADR-0044). A file with secrets needs a secret backend.
func (n *Node) seedDevFile(ctx context.Context, cfg Config) error {
	f := cfg.DevFile
	if f == nil {
		return nil
	}
	if f.HasSecrets() && cfg.OpenBao != nil && cfg.OpenBao.ReadOnly {
		return errors.New("--dev-nodes defines secrets but secrets.openbao.read_only is set")
	}
	if f.HasSecrets() && n.secrets == nil {
		return errors.New("--dev-nodes defines secrets but secrets are disabled: set DAMSON_KEK (damson dev makes a key itself)")
	}
	var w devnodes.SecretWriter
	if n.secrets != nil {
		w = n.secrets
	}
	rep, err := devnodes.Seed(ctx, cfg.Store, w, f, time.Now())
	if err != nil {
		return fmt.Errorf("seeding --dev-nodes: %w", err)
	}
	log := cfg.Logger
	log.Warn("dev nodes: seeded into the inventory and the secret store; development only", "nodes", f.Names())
	log.Info("dev nodes seeded",
		"created", len(rep.Nodes.Created), "updated", len(rep.Nodes.Updated), "deleted", len(rep.Nodes.Deleted), "unchanged", len(rep.Nodes.Unchanged),
		"secrets_written", rep.SecretsWritten, "secrets_unchanged", len(rep.SecretsUnchanged))
	for _, w := range rep.Warnings {
		log.Warn("dev nodes: " + w)
	}
	return nil
}
