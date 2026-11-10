package inventory

import "github.com/denyszorinets/damson/internal/store"

// TargetMap is the plain map a node is for selectors, `targets` and `node`
// expressions, and the frozen run targets. It never carries the credential,
// the host key or its policy, or the port.
func TargetMap(n store.Node) map[string]any {
	tags := make([]any, len(n.Tags))
	for i, t := range n.Tags {
		tags[i] = t
	}
	attrs := make(map[string]any, len(n.Attributes))
	for k, v := range n.Attributes {
		attrs[k] = v
	}
	return map[string]any{
		"id": n.ID, "name": n.Name, "address": n.Address, "tags": tags, "attributes": attrs,
		"pool": n.Pool, "os": n.OS, "transport": n.Transport,
	}
}
