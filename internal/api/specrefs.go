package api

import (
	"fmt"
	"strings"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/secrets"
)

// checkSecretRefs rejects literal secret:// refs in step input that the job's
// project may not use (ARCHITECTURE 9.1): malformed refs, other projects'
// scopes, and _inventory, which is only valid as a node credential. Dispatch
// enforces the same rule; this reports it at authoring time. Messages carry
// the path, never the ref. Refs with ${{ }} are left to dispatch.
func (c *specCheck) checkSecretRefs(job *engine.Job) {
	project := job.Metadata.Project
	var walk func(path string, steps []engine.Step)
	walk = func(path string, steps []engine.Step) {
		for i, st := range steps {
			base := fmt.Sprintf("%s[%d]", path, i)
			c.checkRefsIn(project, base+".with", st.With)
			walk(base+".steps", st.Steps)
			walk(base+".catch", st.Catch)
			walk(base+".finally", st.Finally)
		}
	}
	c.checkRefsIn(project, "spec.options", job.Spec.Options) // default, const and enum of secret options
	walk("spec.steps", job.Spec.Steps)
}

func (c *specCheck) checkRefsIn(project, path string, v any) {
	switch x := v.(type) {
	case string:
		// A ref built from an expression is only known at run time; dispatch
		// authorizes it then.
		if !strings.HasPrefix(x, engine.SecretRefPrefix) || strings.Contains(x, "${{") {
			return
		}
		r, err := secrets.ParseRef(x)
		switch {
		case err != nil:
			c.errors = append(c.errors, SpecIssue{Path: path, Message: "not a valid secret reference"})
		case secrets.Authorize(project, r, false) != nil:
			c.errors = append(c.errors, SpecIssue{Path: path, Message: "secret reference outside the job's project scope (or _inventory, which only a node credential may use)"})
		}
	case map[string]any:
		for k, e := range x {
			c.checkRefsIn(project, path+"."+k, e)
		}
	case []any:
		for i, e := range x {
			c.checkRefsIn(project, fmt.Sprintf("%s[%d]", path, i), e)
		}
	}
}
