package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/secrets"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Errors a NodeSource or SecretSource returns for a name that does not exist.
// They are permanent: the execution fails instead of being requeued forever.
var (
	ErrUnknownNode   = errors.New("unknown node")
	ErrUnknownSecret = errors.New("unknown secret")
	// ErrNodeDisabled is a node that exists but is switched off in the
	// inventory. Permanent as well.
	ErrNodeDisabled = errors.New("node is disabled")
	// ErrHostKeyPolicy is a node whose host key policy the controller does
	// not dispatch: insecure.
	ErrHostKeyPolicy = errors.New("host key policy not available")
	// ErrSecretUnavailable is a secret that exists but cannot be used: a
	// malformed ref, or a version that cannot be decrypted. Permanent too.
	ErrSecretUnavailable = errors.New("secret unavailable")
)

// maxSecretRefs bounds the refs one step.execute may carry.
const maxSecretRefs = 64

// NodeSource resolves the node a single-node step runs on. runID is unused
// until frozen targets arrive with inventory (M4).
type NodeSource interface {
	Node(ctx context.Context, runID, name string) (pluginsdk.NodeInfo, error)
}

// NodeResult is the outcome for one name of a batch: the node, or the error
// Node would have returned for it.
type NodeResult struct {
	Info pluginsdk.NodeInfo
	Err  error
}

// BatchNodeSource is implemented by a NodeSource that resolves many names with
// a constant number of queries per chunk (an inventory step names up to 1000).
// Nodes answers one result per name, in order, with the errors Node returns;
// its own error means the lookup itself failed.
type BatchNodeSource interface {
	Nodes(ctx context.Context, runID string, names []string) ([]NodeResult, error)
}

// PoolResult is the outcome for one name of a pool batch.
type PoolResult struct {
	Pool string
	Err  error
}

// BatchPoolSource is BatchNodeSource for NodePool.
type BatchPoolSource interface {
	NodePools(ctx context.Context, runID string, names []string) ([]PoolResult, error)
}

// PoolSource is implemented by a NodeSource that knows the pool of a node. A
// NodeSource without it routes every step to the default pool.
type PoolSource interface {
	// NodePool returns the pool of the node, "" if it has none, with the same
	// errors as Node for a node that cannot be used.
	NodePool(ctx context.Context, runID, name string) (string, error)
}

// SecretSource resolves one secret:// ref (without a "#field" part) to its
// fields. Values exist only in the returned map: the dispatcher copies them
// into the step.execute bundle and wipes them after the send.
type SecretSource interface {
	Secret(ctx context.Context, runID, ref string) (map[string]string, error)
}

// permanent marks a failure the execution cannot recover from.
type permanent struct{ err error }

func (p *permanent) Error() string { return p.err.Error() }
func (p *permanent) Unwrap() error { return p.err }

// refUse records how a step uses one secret ref: where it came from decides
// which scopes are allowed, and the fields it names must exist.
type refUse struct {
	literal bool // appears in step input
	node    bool // is a targeted node's credential
	fields  map[string]struct{}
}

type refSet map[string]*refUse

func (s refSet) use(ref string) *refUse {
	base, field, hasField := strings.Cut(ref, "#")
	u := s[base]
	if u == nil {
		u = &refUse{fields: map[string]struct{}{}}
		s[base] = u
	}
	if hasField {
		u.fields[field] = struct{}{}
	}
	return u
}

// resolveTargets builds the Nodes and Secrets of a step.execute. With no
// sources configured it returns nothing, as before.
func (d *Dispatcher) resolveTargets(ctx context.Context, runID string, m pluginsdk.Manifest, input map[string]any) (map[string]pluginsdk.NodeInfo, protocol.SecretBundle, error) {
	var nodes map[string]pluginsdk.NodeInfo
	refs := refSet{}
	if d.cfg.Nodes != nil {
		names, err := m.NodeNames(input)
		if err != nil {
			return nil, nil, &permanent{err}
		}
		infos, err := d.resolveNodes(ctx, runID, names)
		if err != nil {
			return nil, nil, err
		}
		for i, name := range names {
			n := infos[i]
			if nodes == nil {
				nodes = map[string]pluginsdk.NodeInfo{}
			}
			nodes[name] = n
			if n.Credential != "" {
				refs.use(n.Credential).node = true
			}
		}
	}
	collectRefs(input, refs)
	if len(refs) == 0 {
		return nodes, nil, nil
	}
	if d.cfg.Secrets == nil {
		return nil, nil, &permanent{errors.New("step uses secret:// refs but no secret source is configured")}
	}
	if len(refs) > maxSecretRefs {
		return nil, nil, &permanent{fmt.Errorf("step uses %d secret refs; at most %d", len(refs), maxSecretRefs)}
	}
	if err := d.authorizeRefs(ctx, runID, refs); err != nil {
		return nil, nil, err
	}
	bundle := protocol.SecretBundle{}
	for ref, use := range refs {
		fields, err := d.cfg.Secrets.Secret(ctx, runID, ref)
		if err != nil {
			wipe(bundle)
			return nil, nil, secretError(ref, err)
		}
		bundle[ref] = fields
		for f := range use.fields {
			if _, ok := fields[f]; !ok {
				wipe(bundle)
				return nil, nil, &permanent{fmt.Errorf("secret %q has no field %q: %w", ref, f, ErrSecretUnavailable)}
			}
		}
	}
	return nodes, bundle, nil
}

// authorizeRefs applies the scope rule of ARCHITECTURE 9.1 before any secret
// is read. A denied ref reads as an unknown one, so it does not reveal whether
// the secret exists. Off unless Config.Project is set (the db backend).
func (d *Dispatcher) authorizeRefs(ctx context.Context, runID string, refs refSet) error {
	if d.cfg.Project == nil {
		return nil
	}
	project, err := d.cfg.Project(ctx, runID)
	if err != nil {
		return fmt.Errorf("resolving the project of run %q: %w", runID, err)
	}
	for ref, use := range refs {
		r, perr := secrets.ParseRef(ref)
		if perr != nil {
			return &permanent{fmt.Errorf("secret %q: %w", ref, ErrSecretUnavailable)}
		}
		// A ref used both ways gets the stricter literal rule.
		if secrets.Authorize(project, r, use.node && !use.literal) != nil {
			return &permanent{fmt.Errorf("secret %q: %w", ref, ErrUnknownSecret)}
		}
	}
	return nil
}

// sourceError keeps only the ref or node name in the message, never what the
// source said, and marks unknown names permanent.
func sourceError(what string, unknown, err error) error {
	if errors.Is(err, unknown) {
		return &permanent{fmt.Errorf("%s: %w", what, unknown)}
	}
	return fmt.Errorf("resolving %s: %w", what, err)
}

// nodeError is sourceError for nodes, which can also be disabled.
func nodeError(name string, err error) error {
	for _, perm := range []error{ErrNodeDisabled, ErrHostKeyPolicy} {
		if errors.Is(err, perm) {
			return &permanent{fmt.Errorf("node %q: %w", name, perm)}
		}
	}
	return sourceError(fmt.Sprintf("node %q", name), ErrUnknownNode, err)
}

// secretError is sourceError for secrets, which also have an unusable state.
func secretError(ref string, err error) error {
	if errors.Is(err, ErrSecretUnavailable) {
		return &permanent{fmt.Errorf("secret %q: %w", ref, ErrSecretUnavailable)}
	}
	return sourceError(fmt.Sprintf("secret %q", ref), ErrUnknownSecret, err)
}

// collectRefs records every secret:// string in v as a literal use.
func collectRefs(v any, refs refSet) {
	switch x := v.(type) {
	case string:
		if strings.HasPrefix(x, "secret://") {
			refs.use(x).literal = true
		}
	case map[string]any:
		for _, e := range x {
			collectRefs(e, refs)
		}
	case []any:
		for _, e := range x {
			collectRefs(e, refs)
		}
	}
}

// wipe clears the secret values of a bundle once the request was sent.
func wipe(b protocol.SecretBundle) {
	for ref, fields := range b {
		clear(fields)
		delete(b, ref)
	}
}

// resolveNodes resolves the names in order and stops at the first one that
// cannot be used, with the same error whichever way the source answers. A
// source that implements BatchNodeSource is asked once for a list of names.
func (d *Dispatcher) resolveNodes(ctx context.Context, runID string, names []string) ([]pluginsdk.NodeInfo, error) {
	out := make([]pluginsdk.NodeInfo, len(names))
	if bs, ok := d.cfg.Nodes.(BatchNodeSource); ok && len(names) > 1 {
		res, err := bs.Nodes(ctx, runID, names)
		if err != nil {
			return nil, err
		}
		if len(res) != len(names) {
			return nil, fmt.Errorf("node source answered %d results for %d names", len(res), len(names))
		}
		for i, r := range res {
			if r.Err != nil {
				return nil, nodeError(names[i], r.Err)
			}
			out[i] = r.Info
		}
		return out, nil
	}
	for i, name := range names {
		n, err := d.cfg.Nodes.Node(ctx, runID, name)
		if err != nil {
			return nil, nodeError(name, err)
		}
		out[i] = n
	}
	return out, nil
}
