package dispatch

import (
	"context"
	"fmt"
	"sort"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
)

// Actor is the audit actor of the events the dispatcher writes.
const Actor = "system:dispatch"

// secretReadDetails is the content of a secret.read event: ids and refs. Never
// a value or a field name (a field name can hint at what a secret holds).
type secretReadDetails struct {
	AttemptID string   `json:"attempt_id"`
	RunID     string   `json:"run_id"`
	Refs      []string `json:"refs"`
}

// auditSecretRead records that the secrets of bundle are about to leave the
// controller for the agent running ex (ARCHITECTURE 18). It is written before
// the values are sent; when it cannot be written the caller must not send.
// One event covers the whole attempt.
func (d *Dispatcher) auditSecretRead(ctx context.Context, ex store.Execution, bundle protocol.SecretBundle) error {
	refs := make([]string, 0, len(bundle))
	for ref := range bundle {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	project := ""
	if d.cfg.Project != nil {
		p, err := d.cfg.Project(ctx, ex.RunID)
		if err != nil {
			return fmt.Errorf("resolving the project of run %s: %w", ex.RunID, err)
		}
		project = p
	}
	ev, err := store.NewEventAs(store.AuditMeta{Actor: Actor}, store.ActionSecretRead, store.AuditTargetExecution, ex.ID, project,
		secretReadDetails{AttemptID: ex.AttemptID, RunID: ex.RunID, Refs: refs}, d.cfg.Now())
	if err != nil {
		return err
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.cfg.CallTimeout)
	defer cancel()
	if err := d.cfg.Store.AppendAuditEvent(actx, ev); err != nil {
		return fmt.Errorf("writing the secret.read event: %w", err)
	}
	return nil
}
