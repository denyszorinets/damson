package agent

import (
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/denyszorinets/damson/internal/protocol"
)

// bundleResolver serves pluginsdk.SecretResolver from the step's secret
// bundle. The bundle exists only here, in memory; wipe drops it when the
// step ends. Error texts name refs and fields, never values.
type bundleResolver struct {
	mu sync.RWMutex
	b  protocol.SecretBundle
}

func newBundleResolver(b protocol.SecretBundle) *bundleResolver {
	return &bundleResolver{b: b}
}

// Resolve returns a single value: the "value" field of the ref, or the field
// named by a "#field" suffix.
func (r *bundleResolver) Resolve(ref string) (string, error) {
	base, field, found := strings.Cut(ref, "#")
	if !found {
		field = "value"
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	fields, ok := r.b[base]
	if !ok {
		return "", fmt.Errorf("secret %q not found", base)
	}
	v, ok := fields[field]
	if !ok {
		return "", fmt.Errorf("secret %q has no field %q", base, field)
	}
	return v, nil
}

// ResolveFields returns a copy of all fields of a whole-credential ref.
func (r *bundleResolver) ResolveFields(ref string) (map[string]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fields, ok := r.b[ref]
	if !ok {
		return nil, fmt.Errorf("secret %q not found", ref)
	}
	return maps.Clone(fields), nil
}

// wipe drops the bundle. Later lookups fail with "not found".
func (r *bundleResolver) wipe() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.b {
		clear(f)
	}
	clear(r.b)
	r.b = nil
}
