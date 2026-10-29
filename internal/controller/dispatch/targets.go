package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Errors a NodeSource or SecretSource returns for a name that does not exist.
// They are permanent: the execution fails instead of being requeued forever.
var (
	ErrUnknownNode   = errors.New("unknown node")
	ErrUnknownSecret = errors.New("unknown secret")
)

// maxSecretRefs bounds the refs one step.execute may carry.
const maxSecretRefs = 64

// NodeSource resolves the node a single-node step runs on. runID is unused
// until frozen targets arrive with inventory (M4).
type NodeSource interface {
	Node(ctx context.Context, runID, name string) (pluginsdk.NodeInfo, error)
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

// resolveTargets builds the Nodes and Secrets of a step.execute. With no
// sources configured it returns nothing, as before.
func (d *Dispatcher) resolveTargets(ctx context.Context, runID string, m pluginsdk.Manifest, input map[string]any) (map[string]pluginsdk.NodeInfo, protocol.SecretBundle, error) {
	var nodes map[string]pluginsdk.NodeInfo
	refs := map[string]struct{}{}
	if d.cfg.Nodes != nil && m.TargetMode == pluginsdk.TargetSingleNode {
		name, ok := input["node"].(string)
		if !ok || name == "" {
			return nil, nil, &permanent{errors.New(`step input "node" must be a node name`)}
		}
		n, err := d.cfg.Nodes.Node(ctx, runID, name)
		if err != nil {
			return nil, nil, sourceError(fmt.Sprintf("node %q", name), ErrUnknownNode, err)
		}
		nodes = map[string]pluginsdk.NodeInfo{name: n}
		if n.Credential != "" {
			refs[baseRef(n.Credential)] = struct{}{}
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
	bundle := protocol.SecretBundle{}
	for ref := range refs {
		fields, err := d.cfg.Secrets.Secret(ctx, runID, ref)
		if err != nil {
			wipe(bundle)
			return nil, nil, sourceError(fmt.Sprintf("secret %q", ref), ErrUnknownSecret, err)
		}
		bundle[ref] = fields
	}
	return nodes, bundle, nil
}

// sourceError keeps only the ref or node name in the message, never what the
// source said, and marks unknown names permanent.
func sourceError(what string, unknown, err error) error {
	if errors.Is(err, unknown) {
		return &permanent{fmt.Errorf("%s: %w", what, unknown)}
	}
	return fmt.Errorf("resolving %s: %w", what, err)
}

func baseRef(ref string) string {
	base, _, _ := strings.Cut(ref, "#")
	return base
}

// collectRefs adds the base ref of every secret:// string in v.
func collectRefs(v any, refs map[string]struct{}) {
	switch x := v.(type) {
	case string:
		if strings.HasPrefix(x, "secret://") {
			refs[baseRef(x)] = struct{}{}
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
