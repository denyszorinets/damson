package api

import (
	"context"
	"net/http"
	"sort"

	"github.com/denyszorinets/damson/internal/store"
)

// inventoryScope is the secret scope of node inventory credentials; it is no
// project, so only an instance admin reaches it.
const inventoryScope = "_inventory"

// maxSecretScopes bounds the exact-scope reads one project listing makes: a
// project and its ancestors.
const maxSecretScopes = 8

// requireScope authorizes a secrets route on its decoded scope (ADR-0077). An
// instance admin may use any scope, orphans and _inventory included. Anyone
// else needs at least need on the project the scope names; a scope that is no
// existing project, and _inventory, read as missing.
func (d deps) requireScope(ctx context.Context, scope, need string) error {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return errForbidden
	}
	if p.InstanceAdmin {
		return nil
	}
	if scope == inventoryScope || store.ValidateProjectPath(scope) != nil {
		return store.ErrNotFound
	}
	if _, err := d.ds.GetProjectByPath(ctx, scope); err != nil {
		return err // a missing project is store.ErrNotFound
	}
	return d.require(ctx, scope, "", need)
}

func secretsBadRequest(ctx context.Context, msg string) error {
	p := newHumaError(http.StatusBadRequest, msg).(*Problem)
	p.RequestID = requestIDFromCtx(ctx)
	return p
}

// authorizeSecretList checks who may list what, before the backend is touched:
// one scope or a project and its ancestors need editor on it, and only an
// instance admin may list every scope.
func (d deps) authorizeSecretList(ctx context.Context, in *listSecretsInput) error {
	switch {
	case in.Scope != "" && in.Project != "":
		return secretsBadRequest(ctx, "give scope or project, not both")
	case in.Scope != "":
		return wrapAPI(ctx, d.requireScope(ctx, in.Scope, store.RoleEditor))
	case in.Project != "":
		return wrapAPI(ctx, d.requireScope(ctx, in.Project, store.RoleEditor))
	}
	if p, ok := PrincipalFrom(ctx); !ok || !p.InstanceAdmin {
		return secretsBadRequest(ctx, "scope or project is required")
	}
	return nil
}

// listSecrets reads one page (plus one row) of secret metadata for an
// authorized list request.
func (d deps) listSecrets(ctx context.Context, in *listSecretsInput, afterScope, afterName string) ([]store.SecretMeta, error) {
	if in.Project != "" {
		return d.listProjectSecrets(ctx, in, afterScope, afterName)
	}
	rows, err := d.secrets.List(ctx, in.Scope, afterScope, afterName, in.Limit+1)
	return rows, wrapAPI(ctx, err)
}

// listProjectSecrets lists the scopes a job of the project may use, in scope
// order with the same keyset as an all-scope listing, one backend read per scope.
func (d deps) listProjectSecrets(ctx context.Context, in *listSecretsInput, afterScope, afterName string) ([]store.SecretMeta, error) {
	scopes := []string{in.Project}
	for p := store.ParentPath(in.Project); p != "" && len(scopes) < maxSecretScopes; p = store.ParentPath(p) {
		scopes = append(scopes, p)
	}
	sort.Strings(scopes)
	var out []store.SecretMeta
	for _, scope := range scopes {
		if scope < afterScope {
			continue
		}
		as, an := "", ""
		if scope == afterScope {
			as, an = afterScope, afterName
		}
		rows, err := d.secrets.List(ctx, scope, as, an, in.Limit+1-len(out))
		if err != nil {
			return nil, apiError(ctx, err)
		}
		out = append(out, rows...)
		if len(out) > in.Limit {
			break
		}
	}
	return out, nil
}

// wrapAPI is apiError for a possibly nil error.
func wrapAPI(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	return apiError(ctx, err)
}
