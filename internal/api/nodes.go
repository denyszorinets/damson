package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/inventory"
	"github.com/denyszorinets/damson/internal/store"
)

// Node is an inventory entry. credential is a secret:// reference, never a
// value; the host key fingerprints are computed from the stored keys.
type Node struct {
	ID                         string         `json:"id"`
	Name                       string         `json:"name"`
	Address                    string         `json:"address"`
	Port                       int            `json:"port"`
	Transport                  string         `json:"transport"`
	OS                         string         `json:"os"`
	Pool                       string         `json:"pool"`
	Tags                       []string       `json:"tags"`
	Attributes                 map[string]any `json:"attributes"`
	Credential                 string         `json:"credential" doc:"A secret:// reference, or empty."`
	HostKey                    string         `json:"host_key" doc:"The pinned key in authorized_keys form, or empty."`
	HostKeyFingerprint         string         `json:"host_key_fingerprint,omitempty"`
	HostKeyPolicy              string         `json:"host_key_policy"`
	HostKeyObserved            string         `json:"host_key_observed" doc:"The key seen by the last TOFU connection, or empty."`
	HostKeyObservedFingerprint string         `json:"host_key_observed_fingerprint,omitempty"`
	HostKeyObservedAt          *time.Time     `json:"host_key_observed_at,omitempty"`
	Source                     string         `json:"source" doc:"manual for nodes made through this API, otherwise the sync source that owns it."`
	ExternalID                 string         `json:"external_id"`
	Enabled                    bool           `json:"enabled"`
	Version                    int64          `json:"version"`
	CreatedAt                  time.Time      `json:"created_at"`
	UpdatedAt                  time.Time      `json:"updated_at"`
}

func toNode(n store.Node) Node {
	tags, attrs := n.Tags, n.Attributes
	if tags == nil {
		tags = []string{}
	}
	if attrs == nil {
		attrs = map[string]any{}
	}
	return Node{
		ID: n.ID, Name: n.Name, Address: n.Address, Port: n.Port, Transport: n.Transport, OS: n.OS, Pool: n.Pool,
		Tags: tags, Attributes: attrs, Credential: n.Credential, HostKey: n.HostKey,
		HostKeyFingerprint: inventory.Fingerprint(n.HostKey), HostKeyPolicy: n.HostKeyPolicy,
		HostKeyObserved: n.HostKeyObserved, HostKeyObservedFingerprint: inventory.Fingerprint(n.HostKeyObserved),
		HostKeyObservedAt: n.HostKeyObservedAt, Source: n.Source, ExternalID: n.ExternalID, Enabled: n.Enabled,
		Version: n.Version, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
	}
}

type nodeBody struct{ Body Node }

type nodeNameInput struct {
	Name string `path:"name" maxLength:"63"`
}

type listNodesInput struct {
	Source  string `query:"source" maxLength:"63" doc:"Only nodes of this source."`
	Enabled string `query:"enabled" enum:"true,false," doc:"Only enabled (true) or only disabled (false) nodes; empty for both."`
	// Selector previews a spec.targets.selector: only nodes it is true for (over
	// the node map, with empty inputs). At most nodeScanLimit nodes are
	// scanned per request, so a page can hold fewer matches than limit and
	// still carry next_cursor.
	Selector string `query:"selector" maxLength:"4096" doc:"A CEL selector as in spec.targets.selector; empty for no filter."`
	Cursor   string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor."`
	Limit    int    `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size."`
}

// nodeScanLimit bounds the rows one selector preview reads.
const nodeScanLimit = 2000

type listNodesOutput struct {
	Body struct {
		Items      []Node `json:"items"`
		NextCursor string `json:"next_cursor,omitempty" doc:"Empty on the last page."`
	}
}

type createNodeInput struct {
	Body struct {
		Name          string         `json:"name" required:"true" maxLength:"63"`
		Address       string         `json:"address" required:"true" maxLength:"253"`
		Port          *int           `json:"port,omitempty" doc:"Defaults to 22 for ssh and 5986 for winrm."`
		Transport     string         `json:"transport,omitempty" doc:"ssh (default) or winrm."`
		OS            string         `json:"os,omitempty"`
		Pool          string         `json:"pool,omitempty"`
		Tags          []string       `json:"tags,omitempty"`
		Attributes    map[string]any `json:"attributes,omitempty"`
		Credential    string         `json:"credential,omitempty" doc:"A secret:// reference to a whole secret."`
		HostKey       string         `json:"host_key,omitempty"`
		HostKeyPolicy string         `json:"host_key_policy,omitempty" doc:"strict (default), tofu or insecure."`
		Enabled       *bool          `json:"enabled,omitempty" doc:"Default true."`
	}
}

// patchNodeInput: every field is optional; name, source and external_id cannot
// be changed here. tags and attributes replace the whole value; an empty
// credential or host_key clears it.
type patchNodeInput struct {
	Name  string `path:"name" maxLength:"63"`
	Force bool   `query:"force" doc:"Edit a node owned by a sync source; its next sync overwrites the edit. The source does not change."`
	Body  struct {
		IfVersion     *int64          `json:"if_version,omitempty" minimum:"1" doc:"The version the caller read; 409 if it moved. Omitted: the version current when handled."`
		Address       *string         `json:"address,omitempty" maxLength:"253"`
		Port          *int            `json:"port,omitempty"`
		Transport     *string         `json:"transport,omitempty"`
		OS            *string         `json:"os,omitempty"`
		Pool          *string         `json:"pool,omitempty"`
		Tags          *[]string       `json:"tags,omitempty"`
		Attributes    *map[string]any `json:"attributes,omitempty"`
		Credential    *string         `json:"credential,omitempty"`
		HostKey       *string         `json:"host_key,omitempty"`
		HostKeyPolicy *string         `json:"host_key_policy,omitempty"`
		Enabled       *bool           `json:"enabled,omitempty"`
	}
}

type deleteNodeInput struct {
	Name      string `path:"name" maxLength:"63"`
	Force     bool   `query:"force" doc:"Delete a node owned by a sync source; the next sync may recreate it."`
	IfVersion int64  `query:"if_version" minimum:"0" default:"0" doc:"The version the caller read; 409 if it moved. 0: the version current when handled."`
}

func registerNodes(api huma.API, d deps) {
	guard := huma.Middlewares{requireAdmin(api, d)}
	op := func(id, method, path, summary string, status int, errs ...int) huma.Operation {
		return huma.Operation{
			OperationID: id, Method: method, Path: path, Summary: summary, Tags: []string{"nodes"},
			Security: adminSecurity, Middlewares: guard, DefaultStatus: status,
			Errors: append([]int{http.StatusUnauthorized, http.StatusServiceUnavailable}, errs...),
		}
	}
	huma.Register(api, op("list-nodes", http.MethodGet, "/nodes", "List inventory nodes", 0, http.StatusBadRequest), func(ctx context.Context, in *listNodesInput) (*listNodesOutput, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		after, err := decodeCursor(ctx, in.Cursor)
		if err != nil {
			return nil, err
		}
		f := store.NodeFilter{Source: in.Source, AfterName: after}
		if in.Enabled != "" {
			on := in.Enabled == "true"
			f.Enabled = &on
		}
		out := &listNodesOutput{}
		out.Body.Items = []Node{}
		if in.Selector == "" {
			f.Limit = in.Limit + 1
			rows, err := d.ds.ListNodes(ctx, f)
			if err != nil {
				return nil, apiError(ctx, err)
			}
			if len(rows) > in.Limit {
				rows = rows[:in.Limit]
				out.Body.NextCursor = encodeCursor(rows[len(rows)-1].Name)
			}
			for _, n := range rows {
				out.Body.Items = append(out.Body.Items, toNode(n))
			}
			return out, nil
		}
		match, err := d.eng.CompileSelector(in.Selector)
		if err != nil {
			return nil, apiError(ctx, invalid("the selector does not compile", ProblemDetail{Message: err.Error(), Location: "query.selector"}))
		}
		return previewNodes(ctx, d, f, match, in.Limit, out)
	})
	huma.Register(api, op("get-node", http.MethodGet, "/nodes/{name}", "Get a node", 0, http.StatusNotFound), func(ctx context.Context, in *nodeNameInput) (*nodeBody, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		n, err := d.ds.GetNode(ctx, in.Name)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		return &nodeBody{Body: toNode(n)}, nil
	})
	huma.Register(api, op("create-node", http.MethodPost, "/nodes", "Create a manual node", http.StatusCreated, http.StatusConflict, http.StatusUnprocessableEntity), func(ctx context.Context, in *createNodeInput) (*nodeBody, error) {
		return createNode(ctx, d, in)
	})
	huma.Register(api, op("patch-node", http.MethodPatch, "/nodes/{name}", "Change a node", 0, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity), func(ctx context.Context, in *patchNodeInput) (*nodeBody, error) {
		return patchNode(ctx, d, in)
	})
	huma.Register(api, op("delete-node", http.MethodDelete, "/nodes/{name}", "Delete a node", http.StatusNoContent, http.StatusNotFound, http.StatusConflict), func(ctx context.Context, in *deleteNodeInput) (*struct{}, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		n, err := d.ds.GetNode(ctx, in.Name)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		if n.Source != inventory.SourceManual && !in.Force {
			return nil, apiError(ctx, inventory.ErrSourceConflict)
		}
		expected := n.Version
		if in.IfVersion != 0 {
			expected = in.IfVersion
		}
		if err := d.ds.DeleteNode(ctx, n.ID, expected); err != nil {
			return nil, apiError(ctx, err)
		}
		return nil, nil
	})
}

func issuesError(is []inventory.Issue) error {
	fields := make([]ProblemDetail, len(is))
	for i, x := range is {
		fields[i] = ProblemDetail{Message: x.Message, Location: "body." + x.Field}
	}
	return invalid("the node is invalid", fields...)
}

func createNode(ctx context.Context, d deps, in *createNodeInput) (*nodeBody, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	b := in.Body
	n := store.Node{
		Name: b.Name, Address: b.Address, Transport: b.Transport, OS: b.OS, Pool: b.Pool, Tags: b.Tags,
		Attributes: b.Attributes, Credential: b.Credential, HostKey: b.HostKey, HostKeyPolicy: b.HostKeyPolicy,
		Source: inventory.SourceManual, Enabled: b.Enabled == nil || *b.Enabled,
	}
	if n.Transport == "" {
		n.Transport = inventory.TransportSSH
	}
	if n.HostKeyPolicy == "" {
		n.HostKeyPolicy = inventory.PolicyStrict
	}
	if b.Port != nil {
		n.Port = *b.Port
	} else {
		n.Port = inventory.DefaultPort(n.Transport)
	}
	if is := inventory.Validate(&n); len(is) > 0 {
		return nil, apiError(ctx, issuesError(is))
	}
	id, err := store.NewID()
	if err != nil {
		return nil, apiError(ctx, err)
	}
	n.ID = id
	created, err := d.ds.CreateNode(ctx, n, d.now())
	if err != nil {
		return nil, apiError(ctx, err)
	}
	return &nodeBody{Body: toNode(created)}, nil
}

// maxPatchAttempts bounds retries of a patch without if_version that races
// another writer.
const maxPatchAttempts = 3

func patchNode(ctx context.Context, d deps, in *patchNodeInput) (*nodeBody, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	for range maxPatchAttempts {
		cur, err := d.ds.GetNode(ctx, in.Name)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		if cur.Source != inventory.SourceManual && !in.Force {
			return nil, apiError(ctx, inventory.ErrSourceConflict)
		}
		expected := cur.Version
		if in.Body.IfVersion != nil {
			if *in.Body.IfVersion != cur.Version {
				return nil, apiError(ctx, store.ErrStale)
			}
			expected = *in.Body.IfVersion
		}
		n := applyPatch(cur, in)
		if is := inventory.Validate(&n); len(is) > 0 {
			return nil, apiError(ctx, issuesError(is))
		}
		if reflect.DeepEqual(n, cur) {
			return &nodeBody{Body: toNode(cur)}, nil // nothing changed: no write, no version bump
		}
		updated, err := d.ds.UpdateNode(ctx, n, expected, d.now())
		if errors.Is(err, store.ErrStale) && in.Body.IfVersion == nil {
			continue // someone else won between our read and write: merge again
		}
		if err != nil {
			return nil, apiError(ctx, err)
		}
		return &nodeBody{Body: toNode(updated)}, nil
	}
	return nil, apiError(ctx, store.ErrStale)
}

func applyPatch(n store.Node, in *patchNodeInput) store.Node {
	b := in.Body
	if b.Address != nil {
		n.Address = *b.Address
	}
	if b.Port != nil {
		n.Port = *b.Port
	}
	if b.Transport != nil {
		// A port still at the default of the old transport follows the change.
		if b.Port == nil && n.Port == inventory.DefaultPort(n.Transport) {
			n.Port = inventory.DefaultPort(*b.Transport)
		}
		n.Transport = *b.Transport
	}
	if b.OS != nil {
		n.OS = *b.OS
	}
	if b.Pool != nil {
		n.Pool = *b.Pool
	}
	if b.Tags != nil {
		n.Tags = *b.Tags
	}
	if b.Attributes != nil {
		n.Attributes = *b.Attributes
	}
	if b.Credential != nil {
		n.Credential = *b.Credential
	}
	if b.HostKey != nil {
		n.HostKey = *b.HostKey
	}
	if b.HostKeyPolicy != nil {
		n.HostKeyPolicy = *b.HostKeyPolicy
	}
	if b.Enabled != nil {
		n.Enabled = *b.Enabled
	}
	return n
}

// previewNodes pages through the inventory, keeping the nodes the selector
// matches, until the page is full or nodeScanLimit rows were read. The cursor
// is the last name scanned, so the next request resumes after it.
func previewNodes(ctx context.Context, d deps, f store.NodeFilter, match engine.TargetMatcher, limit int, out *listNodesOutput) (*listNodesOutput, error) {
	scanned := 0
	last := ""
	for {
		f.Limit = 200
		rows, err := d.ds.ListNodes(ctx, f)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		for _, n := range rows {
			scanned++
			last = n.Name
			if match(map[string]any{}, inventory.TargetMap(n)) {
				out.Body.Items = append(out.Body.Items, toNode(n))
			}
			if len(out.Body.Items) == limit || scanned >= nodeScanLimit {
				// Stopped early: resume after the last node looked at, unless
				// that was the end of the inventory (the next call returns none).
				out.Body.NextCursor = encodeCursor(last)
				return out, nil
			}
		}
		if len(rows) < 200 {
			return out, nil
		}
		f.AfterName = last
	}
}
