package advance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/denyszorinets/damson/internal/engine"
)

// fillIdempotent sets state.IdempotentPlugins for the plugins of steps whose
// latest attempt was lost: only then does the engine need to know whether the
// plugin may be retried (retry.on_lost unset follows the manifest's
// idempotent flag). The answer is true when at least one stored manifest
// matches the step's plugin reference ("name" or "name@<major>") and every
// match is idempotent; no manifest means false.
func (c *core) fillIdempotent(ctx context.Context, state *engine.RunState, job *engine.Job) error {
	refs := lostPluginRefs(*state, job)
	if len(refs) == 0 {
		return nil
	}
	rows, err := c.st.ListPluginManifests(ctx)
	if err != nil {
		return fmt.Errorf("reading plugin manifests: %w", err)
	}
	type manifest struct {
		Name       string `json:"name"`
		Version    string `json:"version"`
		Idempotent bool   `json:"idempotent"`
	}
	var ms []manifest
	for _, r := range rows {
		var m manifest
		if err := json.Unmarshal(r.Manifest, &m); err != nil {
			continue // a manifest we cannot read proves nothing
		}
		ms = append(ms, m)
	}
	state.IdempotentPlugins = make(map[string]bool, len(refs))
	for _, ref := range refs {
		name, major, _ := strings.Cut(ref, "@")
		matched, all := false, true
		for _, m := range ms {
			if m.Name != name || (major != "" && majorOf(m.Version) != major) {
				continue
			}
			matched = true
			all = all && m.Idempotent
		}
		state.IdempotentPlugins[ref] = matched && all
	}
	return nil
}

// lostPluginRefs lists the plugin references of the open steps whose latest
// attempt is lost.
func lostPluginRefs(state engine.RunState, job *engine.Job) []string {
	plugins := map[string]string{}
	for _, s := range job.Spec.Steps {
		plugins[s.ID] = s.Plugin
	}
	var refs []string
	seen := map[string]bool{}
	for _, inst := range state.Steps {
		if inst.Status.Terminal() || len(inst.Executions) == 0 {
			continue
		}
		latest := inst.Executions[0]
		for _, ex := range inst.Executions {
			if ex.Attempt > latest.Attempt {
				latest = ex
			}
		}
		ref := plugins[inst.StepID]
		if latest.State == engine.ExecLost && ref != "" && !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	return refs
}

// majorOf is the part of a semantic version before the first dot.
func majorOf(version string) string {
	major, _, _ := strings.Cut(strings.TrimPrefix(version, "v"), ".")
	return major
}
