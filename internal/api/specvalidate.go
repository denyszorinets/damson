package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// SpecIssue is one located problem (or warning) in a job spec. Path is the
// spec path such as "spec.steps[1].with.host". Messages never carry values
// from the spec.
type SpecIssue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// specCheck is the outcome of validating a spec document.
type specCheck struct {
	job      *engine.Job // nil when the spec has errors
	errors   []SpecIssue
	warnings []SpecIssue
}

// checkSpec validates a spec the way a job is authored: engine.Parse (schema,
// structure, expressions), then each plugin step's `with` against the input
// schema of the highest known version of its plugin. A plugin the controller
// does not know yet is a warning, not an error: its agents may simply not be
// connected.
func (d deps) checkSpec(raw json.RawMessage) specCheck {
	var c specCheck
	if len(strings.TrimSpace(string(raw))) == 0 {
		c.errors = []SpecIssue{{Path: "$", Message: "spec is required"}}
		return c
	}
	job, err := d.eng.Parse(raw)
	if err != nil {
		var ve engine.ValidationErrors
		if errors.As(err, &ve) {
			for _, fe := range ve {
				c.errors = append(c.errors, SpecIssue{Path: fe.Path, Message: fe.Message})
			}
		} else {
			c.errors = []SpecIssue{{Path: "$", Message: "the spec could not be decoded"}}
		}
		return c
	}
	if d.plugins != nil {
		c.checkPlugins(d.plugins, job)
	}
	if len(c.errors) == 0 {
		c.job = job
	}
	return c
}

func (c *specCheck) checkPlugins(pc PluginCatalog, job *engine.Job) {
	for i, st := range job.Spec.Steps {
		if st.IsGroup() {
			continue
		}
		base := fmt.Sprintf("spec.steps[%d]", i)
		m, err := pc.Lookup(st.Plugin)
		switch {
		case errors.Is(err, pluginreg.ErrUnknownPlugin), errors.Is(err, pluginreg.ErrNoVersion):
			c.warnings = append(c.warnings, SpecIssue{Path: base + ".plugin",
				Message: fmt.Sprintf("plugin %q is not known to this controller (no agent has reported a matching version); its input was not checked", st.Plugin)})
			continue
		case err != nil:
			c.errors = append(c.errors, SpecIssue{Path: base + ".plugin", Message: "the plugin reference is invalid"})
			continue
		}
		with := st.With
		if with == nil {
			with = map[string]any{}
		}
		input, err := json.Marshal(with)
		if err != nil {
			c.errors = append(c.errors, SpecIssue{Path: base + ".with", Message: "the plugin input could not be encoded"})
			continue
		}
		c.checkInput(pc, m.Name, base+".with", m, with, input)
	}
}

func (c *specCheck) checkInput(pc PluginCatalog, name, base string, m pluginsdk.Manifest, with map[string]any, input []byte) {
	err := pc.ValidateInput(m, input)
	var ve pluginreg.ValidationErrors
	switch {
	case err == nil:
	case errors.As(err, &ve):
		for _, fe := range ve {
			if underExpression(with, fe.Path) {
				continue // known only once resolved: the trigger validates the resolved input (when the plugin is known), dispatch validates again
			}
			c.errors = append(c.errors, SpecIssue{Path: joinPath(base, fe.Path), Message: fe.Message})
		}
	default:
		c.warnings = append(c.warnings, SpecIssue{Path: base,
			Message: fmt.Sprintf("the input of plugin %q could not be checked against its schema", name)})
	}
}

// joinPath appends a pluginreg field path ("$", "a.b[0]") to base.
func joinPath(base, p string) string {
	switch {
	case p == "" || p == "$":
		return base
	case strings.HasPrefix(p, "["):
		return base + p
	}
	return base + "." + p
}

// underExpression reports whether the value at path in with, or any value on
// the way to it, is a string holding a ${{ }} expression. Such a value is
// only known when the step is created, so a schema failure there is not an
// authoring error; the trigger and dispatch check the resolved input.
func underExpression(with map[string]any, path string) bool {
	var cur any = with
	if path == "" || path == "$" {
		return false
	}
	for _, tok := range pathTokens(path) {
		switch t := cur.(type) {
		case string:
			return strings.Contains(t, "${{")
		case map[string]any:
			next, ok := t[tok]
			if !ok {
				return false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(t) {
				return false
			}
			cur = t[i]
		default:
			return false
		}
	}
	s, ok := cur.(string)
	return ok && strings.Contains(s, "${{")
}

// pathTokens splits "a.b[0].c" into [a b 0 c].
func pathTokens(p string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, r := range p {
		switch r {
		case '.', '[':
			flush()
		case ']':
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return out
}

// problemDetails converts issues to problem+json field errors.
func problemDetails(issues []SpecIssue) []ProblemDetail {
	out := make([]ProblemDetail, len(issues))
	for i, is := range issues {
		out[i] = ProblemDetail{Message: is.Message, Location: is.Path}
	}
	return out
}
