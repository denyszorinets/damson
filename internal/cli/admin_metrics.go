package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

// metricsTokenCreate makes a new metrics token and replaces any existing one,
// which stops working at once. The token reads /metrics and nothing else. Only
// the hash is stored.
func metricsTokenCreate(ctx context.Context, st store.Store, c adminCmd, now time.Time) error {
	plain, hash, err := tokens.GenerateWithPrefix(rand.Reader, tokens.MetricsPrefix)
	if err != nil {
		return err
	}
	cur, err := st.GetSetting(ctx, tokens.MetricsSettingKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
		_, err = st.CreateSetting(ctx, tokens.MetricsSettingKey, hash)
	case err == nil:
		_, err = st.UpdateSetting(ctx, tokens.MetricsSettingKey, hash, cur.Version)
	}
	if err != nil {
		return fmt.Errorf("storing metrics token: %w", err)
	}
	if err := auditMetricsToken(ctx, st, store.ActionMetricsTokenCreate, now); err != nil {
		return err
	}
	if c.json {
		err = writeJSON(c.stdout, map[string]string{"token": plain})
	} else {
		_, err = fmt.Fprintln(c.stdout, plain)
	}
	if err != nil {
		return fmt.Errorf("writing token: %w", err)
	}
	fmt.Fprintln(c.stderr, "Store this token now: it is shown only once. Any earlier metrics token no longer works.")
	return nil
}

// metricsTokenRevoke disables the metrics token: /metrics answers 401 until a
// new token is created.
func metricsTokenRevoke(ctx context.Context, st store.Store, c adminCmd, now time.Time) error {
	cur, err := st.GetSetting(ctx, tokens.MetricsSettingKey)
	if errors.Is(err, store.ErrNotFound) || (err == nil && cur.Value == "") {
		return errors.New("there is no metrics token")
	}
	if err == nil {
		_, err = st.UpdateSetting(ctx, tokens.MetricsSettingKey, "", cur.Version)
	}
	if err != nil {
		return fmt.Errorf("revoking metrics token: %w", err)
	}
	if err := auditMetricsToken(ctx, st, store.ActionMetricsTokenRevoke, now); err != nil {
		return err
	}
	if c.json {
		return writeJSON(c.stdout, map[string]string{"status": "revoked"})
	}
	_, err = fmt.Fprintln(c.stdout, "metrics token revoked")
	return err
}

func auditMetricsToken(ctx context.Context, st store.Store, action string, now time.Time) error {
	ev, err := store.NewMetricsTokenEvent(store.WithAudit(ctx, store.AuditMeta{Actor: cliActor()}), action, now)
	if err == nil {
		err = st.AppendAuditEvent(ctx, ev)
	}
	if err != nil {
		return fmt.Errorf("the metrics token changed but recording the audit event failed: %w", err)
	}
	return nil
}
