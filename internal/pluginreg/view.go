package pluginreg

import (
	"slices"
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Conflict is a minority report for a name+version: agents whose manifest
// checksum differs from the accepted one. They are never dispatched to.
type Conflict struct {
	Checksum string
	Agents   []Provider
}

// Version is one accepted name+version.
type Version struct {
	Manifest pluginsdk.Manifest
	Checksum string
	// Builtin is true when the controller links this exact manifest itself.
	Builtin bool
	// Agents run the accepted checksum, sorted by id.
	Agents []Provider
	// Conflicts lists agents reporting a different checksum for this
	// name+version (ARCHITECTURE section 6, schema conflicts).
	Conflicts []Conflict

	caps map[string]map[string]struct{} // agent id -> capability set
}

// Plugin is every known version of one plugin name, highest first.
//
// A Plugin and its slices are shared with the registry's cache: treat them as
// read-only.
type Plugin struct {
	Name string
	// HasBuiltin is true when the controller links a version of this name.
	// Unpinned references to such a name resolve only to built-in versions.
	HasBuiltin bool
	Versions   []Version
}

// Pools returns the sorted distinct pools of the agents running v.
func (v Version) Pools() []string {
	var pools []string
	for _, a := range v.Agents {
		if !slices.Contains(pools, a.Pool) {
			pools = append(pools, a.Pool)
		}
	}
	slices.Sort(pools)
	return pools
}

// snapshot returns the derived plugin view, rebuilding it only after a
// SetAgent or RemoveAgent invalidated it.
func (r *Registry) snapshot() map[string]*Plugin {
	r.mu.RLock()
	c := r.cache
	r.mu.RUnlock()
	if c != nil {
		return c
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil {
		r.cache = r.build()
	}
	return r.cache
}

// build derives the view; the caller holds r.mu.
func (r *Registry) build() map[string]*Plugin {
	groups := map[pluginKey]map[string]*Version{}
	get := func(k pluginKey, sum string, m pluginsdk.Manifest) *Version {
		g := groups[k]
		if g == nil {
			g = map[string]*Version{}
			groups[k] = g
		}
		v := g[sum]
		if v == nil {
			v = &Version{Manifest: m, Checksum: sum, caps: map[string]map[string]struct{}{}}
			g[sum] = v
		}
		return v
	}
	for k, b := range r.builtins {
		get(k, b.sum, b.man).Builtin = true
	}
	for _, a := range r.agents {
		for k, sum := range a.sums {
			v := get(k, sum, a.mans[k])
			v.Agents = append(v.Agents, Provider{AgentID: a.id, Pool: a.pool})
			v.caps[a.id] = a.caps
		}
	}
	out := map[string]*Plugin{}
	for k, g := range groups {
		p := out[k.name]
		if p == nil {
			_, hasBuiltin := r.builtinTop[k.name]
			p = &Plugin{Name: k.name, HasBuiltin: hasBuiltin}
			out[k.name] = p
		}
		p.Versions = append(p.Versions, accept(g))
	}
	for _, p := range out {
		slices.SortFunc(p.Versions, func(a, b Version) int {
			return compareSemver(b.Manifest.Version, a.Manifest.Version)
		})
	}
	return out
}

// accept picks the checksum that wins a name+version: the one the controller
// links itself, else the most reported, ties going to the smaller checksum so
// the choice is deterministic. The others become Conflicts.
func accept(g map[string]*Version) Version {
	var best *Version
	for _, v := range g {
		if best == nil || beats(v, best) {
			best = v
		}
	}
	for _, v := range g {
		if v != best {
			best.Conflicts = append(best.Conflicts, Conflict{Checksum: v.Checksum, Agents: sortedProviders(v.Agents)})
		}
	}
	slices.SortFunc(best.Conflicts, func(a, b Conflict) int { return strings.Compare(a.Checksum, b.Checksum) })
	best.Agents = sortedProviders(best.Agents)
	return *best
}

func beats(a, b *Version) bool {
	switch {
	case a.Builtin != b.Builtin:
		return a.Builtin
	case len(a.Agents) != len(b.Agents):
		return len(a.Agents) > len(b.Agents)
	}
	return a.Checksum < b.Checksum
}

func sortedProviders(ps []Provider) []Provider {
	out := slices.Clone(ps)
	slices.SortFunc(out, func(a, b Provider) int { return strings.Compare(a.AgentID, b.AgentID) })
	return out
}

// List returns every known plugin sorted by name.
func (r *Registry) List() []Plugin {
	snap := r.snapshot()
	out := make([]Plugin, 0, len(snap))
	for _, p := range snap {
		out = append(out, *p)
	}
	slices.SortFunc(out, func(a, b Plugin) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Get returns one plugin by name.
func (r *Registry) Get(name string) (Plugin, bool) {
	p, ok := r.snapshot()[name]
	if !ok {
		return Plugin{}, false
	}
	return *p, true
}
