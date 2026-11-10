package pluginreg

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Resolution errors. Use errors.Is.
var (
	ErrInvalidRef     = errors.New("invalid plugin reference")
	ErrUnknownPlugin  = errors.New("unknown plugin")
	ErrNoVersion      = errors.New("no plugin version matches the pin")
	ErrNoCapableAgent = errors.New("no connected agent can run the plugin")
)

// Ref is a parsed "name" or "name@major" reference.
type Ref struct {
	Name   string
	Major  uint64
	Pinned bool
}

// ParseRef parses a step's plugin field.
func ParseRef(s string) (Ref, error) {
	name, pin, pinned := strings.Cut(s, "@")
	if name == "" || strings.ContainsAny(name, " \t@") {
		return Ref{}, fmt.Errorf("%w: %q", ErrInvalidRef, s)
	}
	if !pinned {
		return Ref{Name: name}, nil
	}
	n, err := strconv.ParseUint(pin, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != pin {
		return Ref{}, fmt.Errorf("%w: %q (the pin must be a major version such as helm@1)", ErrInvalidRef, s)
	}
	return Ref{Name: name, Major: n, Pinned: true}, nil
}

func (r Ref) matches(version string) bool {
	if !r.Pinned {
		return true
	}
	m, ok := majorOf(version)
	return ok && m == r.Major
}

// Filter restricts the agents considered by Resolve.
type Filter struct {
	Pool string // pool name; empty means any pool
}

// Resolution is the outcome of Resolve.
type Resolution struct {
	Manifest pluginsdk.Manifest
	Checksum string
	// Agents are the capable connected agents, sorted by id. Never empty.
	Agents []Provider
}

// eligible applies the shadowing rule: an unpinned reference to a name the
// controller links itself resolves only to a built-in version, so an agent
// cannot take over a built-in by reporting a higher version. Agent-only
// versions of such a name need an explicit pin (name@major).
func (p Plugin) eligible(ref Ref, v Version) bool {
	return ref.matches(v.Manifest.Version) && (ref.Pinned || !p.HasBuiltin || v.Builtin)
}

// Lookup returns the highest known version matching ref, whether or not an
// agent can run it right now. It is for authoring time only (job validation,
// UI forms): it may name a version no connected agent can run. The dispatch
// path must not use it; it must validate against the manifest that Resolve
// returns, because that is the version an agent will actually execute.
func (r *Registry) Lookup(ref string) (pluginsdk.Manifest, error) {
	p, parsed, err := r.find(ref)
	if err != nil {
		return pluginsdk.Manifest{}, err
	}
	for _, v := range p.Versions {
		if p.eligible(parsed, v) {
			return v.Manifest, nil
		}
	}
	return pluginsdk.Manifest{}, noVersion(ref, p, parsed)
}

func noVersion(ref string, p Plugin, parsed Ref) error {
	if !parsed.Pinned && p.HasBuiltin {
		return fmt.Errorf("%w: %s (unpinned references resolve only to built-in versions; pin a major such as %s@1)", ErrNoVersion, ref, ref)
	}
	return fmt.Errorf("%w: %s", ErrNoVersion, ref)
}

// Resolve picks the highest version matching ref that at least one eligible
// agent can run (ARCHITECTURE section 6: unpinned means the highest version
// available on eligible agents). An agent is eligible when it is in the pool
// filter, reports the accepted checksum of the manifest (agents on the
// minority side of a schema conflict are refused) and has every capability in
// Requires.
func (r *Registry) Resolve(ref string, f Filter) (Resolution, error) {
	p, parsed, err := r.find(ref)
	if err != nil {
		return Resolution{}, err
	}
	matched := false
	for _, v := range p.Versions {
		if !p.eligible(parsed, v) {
			continue
		}
		matched = true
		if agents := v.capable(f); len(agents) > 0 {
			return Resolution{Manifest: v.Manifest, Checksum: v.Checksum, Agents: agents}, nil
		}
	}
	if !matched {
		return Resolution{}, noVersion(ref, p, parsed)
	}
	note := ""
	if f.Pool != "" {
		note = " in pool " + strconv.Quote(f.Pool)
	}
	return Resolution{}, fmt.Errorf("%w: %s%s", ErrNoCapableAgent, ref, note)
}

func (r *Registry) find(ref string) (Plugin, Ref, error) {
	parsed, err := ParseRef(ref)
	if err != nil {
		return Plugin{}, Ref{}, err
	}
	p, ok := r.Get(parsed.Name)
	if !ok {
		return Plugin{}, Ref{}, fmt.Errorf("%w: %s", ErrUnknownPlugin, parsed.Name)
	}
	return p, parsed, nil
}

// capable filters v.Agents by pool and Requires.
func (v Version) capable(f Filter) []Provider {
	var out []Provider
	for _, a := range v.Agents {
		if f.Pool != "" && a.Pool != f.Pool {
			continue
		}
		have := v.caps[a.AgentID]
		ok := true
		for _, need := range v.Manifest.Requires {
			if _, has := have[need]; !has {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, a)
		}
	}
	return out
}
