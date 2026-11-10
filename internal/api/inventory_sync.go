package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/inventory"
	"github.com/denyszorinets/damson/internal/store"
)

// SyncNode is a node as an external source describes it. external_id is the
// source's own identifier; without it nodes match by name.
type SyncNode struct {
	Name          string         `json:"name" required:"true" maxLength:"63"`
	ExternalID    string         `json:"external_id,omitempty" maxLength:"255"`
	Address       string         `json:"address" required:"true" maxLength:"253"`
	Port          *int           `json:"port,omitempty" doc:"Defaults to 22 for ssh and 5986 for winrm."`
	Transport     string         `json:"transport,omitempty" doc:"ssh (default) or winrm."`
	OS            string         `json:"os,omitempty"`
	Pool          string         `json:"pool,omitempty"`
	Tags          []string       `json:"tags,omitempty"`
	Attributes    map[string]any `json:"attributes,omitempty"`
	Credential    string         `json:"credential,omitempty" doc:"A secret:// reference to a whole secret."`
	HostKey       string         `json:"host_key,omitempty" doc:"The source owns the pinned key; an observed key is never touched."`
	HostKeyPolicy string         `json:"host_key_policy,omitempty" doc:"strict (default), tofu or insecure."`
	Enabled       *bool          `json:"enabled,omitempty" doc:"Default true."`
}

func (s SyncNode) toNode() store.Node {
	n := store.Node{
		Name: s.Name, ExternalID: s.ExternalID, Address: s.Address, Transport: s.Transport, OS: s.OS, Pool: s.Pool,
		Tags: s.Tags, Attributes: s.Attributes, Credential: s.Credential, HostKey: s.HostKey,
		HostKeyPolicy: s.HostKeyPolicy, Enabled: s.Enabled == nil || *s.Enabled,
	}
	if n.Transport == "" {
		n.Transport = inventory.TransportSSH
	}
	if n.HostKeyPolicy == "" {
		n.HostKeyPolicy = inventory.PolicyStrict
	}
	if s.Port != nil {
		n.Port = *s.Port
	} else {
		n.Port = inventory.DefaultPort(n.Transport)
	}
	return n
}

func syncNodes(in []SyncNode) []store.Node {
	out := make([]store.Node, len(in))
	for i, s := range in {
		out[i] = s.toNode()
	}
	return out
}

// SyncResult is the outcome of a sync or batch, by node name.
type SyncResult struct {
	DryRun    bool           `json:"dry_run" doc:"True: nothing was written."`
	Created   []string       `json:"created"`
	Updated   []string       `json:"updated"`
	Deleted   []string       `json:"deleted"`
	Unchanged []string       `json:"unchanged"`
	Conflicts []SyncConflict `json:"conflicts" doc:"Always empty in a 200: a conflict is a 409, also in a dry run."`
}

// SyncConflict is a desired name that another source (or a node that stays) holds.
type SyncConflict struct {
	Name    string `json:"name"`
	OwnedBy string `json:"owned_by"`
}

func toSyncResult(d inventory.Diff, dry bool) SyncResult {
	nn := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	r := SyncResult{DryRun: dry, Created: nn(d.Created), Updated: nn(d.Updated), Deleted: nn(d.Deleted), Unchanged: nn(d.Unchanged), Conflicts: []SyncConflict{}}
	for _, c := range d.Conflicts {
		r.Conflicts = append(r.Conflicts, SyncConflict{Name: c.Name, OwnedBy: c.OwnedBy})
	}
	return r
}

type syncInput struct {
	Source string `path:"source" maxLength:"63"`
	DryRun bool   `query:"dry_run" doc:"Report what would change; write nothing."`
	Prune  bool   `query:"prune" doc:"Delete the nodes of this source that are not in the body."`
	Body   struct {
		Nodes []SyncNode `json:"nodes" required:"true" nullable:"false" maxItems:"5000" doc:"The full desired state of the source."`
	}
}

// DeleteNodeRef names a node of the source to delete.
type DeleteNodeRef struct {
	ExternalID string `json:"external_id,omitempty" maxLength:"255"`
	Name       string `json:"name,omitempty" maxLength:"63" doc:"Used when there is no external_id."`
}

type batchInput struct {
	Source string `path:"source" maxLength:"63"`
	DryRun bool   `query:"dry_run"`
	Body   struct {
		Upserts []SyncNode      `json:"upserts,omitempty" nullable:"false" maxItems:"5000"`
		Deletes []DeleteNodeRef `json:"deletes,omitempty" nullable:"false" maxItems:"5000" doc:"Nodes of this source to delete; one that is not there is ignored."`
	}
}

// Resolve refuses a flag that is present but empty (?dry_run): Huma would read
// it as false, and a safety flag must not silently turn into a real write.
func (in *syncInput) Resolve(ctx huma.Context) []error  { return emptyFlags(ctx, "dry_run", "prune") }
func (in *batchInput) Resolve(ctx huma.Context) []error { return emptyFlags(ctx, "dry_run") }

func emptyFlags(ctx huma.Context, names ...string) []error {
	u := ctx.URL()
	q := u.Query()
	var errs []error
	for _, n := range names {
		if q.Has(n) && q.Get(n) == "" {
			errs = append(errs, &huma.ErrorDetail{Message: "give true or false", Location: "query." + n})
		}
	}
	return errs
}

type syncOutput struct{ Body SyncResult }

func registerInventorySync(api huma.API, d deps) {
	guard := huma.Middlewares{requireAdmin(api, d)}
	op := func(id, method, path, summary string) huma.Operation {
		return huma.Operation{
			OperationID: id, Method: method, Path: path, Summary: summary, Tags: []string{"inventory"},
			Security: adminSecurity, Middlewares: guard, MaxBodyBytes: 16 << 20, BodyReadTimeout: 30 * time.Second,
			Errors: []int{http.StatusUnauthorized, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
		}
	}
	put := op("sync-nodes", http.MethodPut, "/inventory/sources/{source}/nodes", "Full-state sync of one source")
	put.Description = "Makes the nodes of the source equal to the body in one transaction: nodes are matched by external_id, then by name; unchanged nodes are not written and keep their version; with prune, the source's other nodes are deleted. Nodes of other sources are never touched; a name they hold is a 409 conflict and nothing is written."
	huma.Register(api, put, func(ctx context.Context, in *syncInput) (*syncOutput, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		if err := inventory.ValidateSource(in.Source); err != nil {
			return nil, apiError(ctx, invalid("the source is invalid", ProblemDetail{Message: err.Error(), Location: "path.source"}))
		}
		diff, err := inventory.Sync(ctx, d.ds, in.Source, syncNodes(in.Body.Nodes), in.Prune, in.DryRun, d.now())
		return syncResult(ctx, diff, err, in.DryRun)
	})
	post := op("batch-nodes", http.MethodPost, "/inventory/sources/{source}/nodes:batch", "Incremental upserts and deletes for one source")
	post.Description = "Upserts as the full-state sync does, without pruning, plus deletes of this source's nodes by external_id or name. A node cannot be upserted and deleted in one call."
	huma.Register(api, post, func(ctx context.Context, in *batchInput) (*syncOutput, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		if err := inventory.ValidateSource(in.Source); err != nil {
			return nil, apiError(ctx, invalid("the source is invalid", ProblemDetail{Message: err.Error(), Location: "path.source"}))
		}
		dels := make([]inventory.DeleteRef, len(in.Body.Deletes))
		for i, x := range in.Body.Deletes {
			dels[i] = inventory.DeleteRef{ExternalID: x.ExternalID, Name: x.Name}
		}
		diff, err := inventory.Batch(ctx, d.ds, in.Source, syncNodes(in.Body.Upserts), dels, in.DryRun, d.now())
		return syncResult(ctx, diff, err, in.DryRun)
	})
}

// syncResult maps the outcome of a sync to the response or a problem.
func syncResult(ctx context.Context, diff inventory.Diff, err error, dry bool) (*syncOutput, error) {
	switch {
	case err == nil:
		return &syncOutput{Body: toSyncResult(diff, dry)}, nil
	case errors.Is(err, inventory.ErrSyncConflict):
		return nil, apiError(ctx, &syncConflictError{conflicts: diff.Conflicts})
	case errors.Is(err, inventory.ErrBadDesired):
		return nil, apiError(ctx, invalid(err.Error()))
	}
	return nil, apiError(ctx, err)
}

// syncConflictError is a sync refused because of name clashes.
type syncConflictError struct{ conflicts []inventory.Conflict }

func (e *syncConflictError) Error() string { return inventory.ErrSyncConflict.Error() }
