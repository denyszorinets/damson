package engine

import (
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/denyszorinets/damson/internal/expr"
)

var (
	stepIDPattern = regexp.MustCompile(`^[a-z0-9-]+$`)
	// "name" or "name@<major>".
	pluginPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(@[0-9]+)?$`)
)

// Validate checks the semantic rules the JSON Schema cannot express. Paths
// are relative to the document root, e.g. "spec.steps[1].when". It returns
// nil for a valid job.
func Validate(j *Job) ValidationErrors {
	env, err := expr.NewEnv(expr.Limits{})
	if err != nil {
		return ValidationErrors{{Path: "$", Message: err.Error()}}
	}
	v := &validator{env: env}
	v.job(j)
	return v.errs
}

type validator struct {
	env  *expr.Env
	errs ValidationErrors
}

func (v *validator) add(path, format string, args ...any) {
	v.errs = append(v.errs, FieldError{Path: path, Message: fmt.Sprintf(format, args...)})
}

func (v *validator) job(j *Job) {
	if j.APIVersion != APIVersion {
		v.add("apiVersion", "must be %q", APIVersion)
	}
	if j.Kind != KindJob {
		v.add("kind", "must be %q", KindJob)
	}
	v.spec(&j.Spec)
}

func (v *validator) spec(s *Spec) {
	if s.Options != nil {
		v.options(s.Options)
	}
	if s.Targets != nil && s.Targets.Selector != "" {
		v.expression("spec.targets.selector", s.Targets.Selector)
	}
	if s.Concurrency != nil {
		v.errs = append(v.errs, errNotSupported("spec.concurrency", "concurrency"))
	}
	v.duration("spec.timeout", s.Timeout)
	if len(s.Steps) == 0 {
		v.add("spec.steps", "must contain at least one step")
	}
	v.steps("spec.steps", s.Steps)
}

// options checks that spec.options is itself a valid JSON Schema.
func (v *validator) options(opts map[string]any) {
	if t, ok := opts["type"]; ok && t != "object" {
		v.add("spec.options.type", "must be \"object\"")
		return
	}
	c := newSchemaCompiler()
	if err := c.AddResource("damson:options", opts); err != nil {
		v.add("spec.options", "not a valid JSON Schema: %v", err)
		return
	}
	if _, err := c.Compile("damson:options"); err != nil {
		v.add("spec.options", "not a valid JSON Schema: %v", err)
		return
	}
	v.checkSecretSchema("spec.options", opts, false)
}

func (v *validator) steps(path string, steps []Step) {
	seen := map[string]int{}
	for i := range steps {
		p := fmt.Sprintf("%s[%d]", path, i)
		s := &steps[i]
		switch first, dup := seen[s.ID]; {
		case s.ID == "":
			v.add(p+".id", "is required")
		case !stepIDPattern.MatchString(s.ID):
			v.add(p+".id", "%q must match [a-z0-9-]+", s.ID)
		case dup:
			v.add(p+".id", "duplicate id %q (first used at %s[%d])", s.ID, path, first)
		}
		if s.ID != "" {
			if _, ok := seen[s.ID]; !ok {
				seen[s.ID] = i
			}
		}
		v.step(p, s)
	}
}

func (v *validator) step(p string, s *Step) {
	v.reserved(p, s)
	if s.IsGroup() {
		return
	}
	switch {
	case s.Plugin == "":
		v.add(p+".plugin", "is required for a plugin step")
	case !pluginPattern.MatchString(s.Plugin):
		v.add(p+".plugin", "%q must be \"name\" or \"name@<major>\"", s.Plugin)
	}
	v.duration(p+".timeout", s.Timeout)
	if s.When != "" {
		v.expression(p+".when", s.When)
	}
	if s.RunsOn != nil && s.RunsOn.Pool == "" && len(s.RunsOn.Labels) == 0 {
		v.add(p+".runs_on", "must set pool or labels")
	}
	v.templates(p+".with", s.With)
}

// reserved rejects fields that are parsed for forward compatibility but not
// implemented in this milestone.
func (v *validator) reserved(p string, s *Step) {
	fields := []struct {
		name string
		set  bool
	}{
		{"steps", len(s.Steps) > 0}, {"catch", len(s.Catch) > 0}, {"finally", len(s.Finally) > 0},
		{"rethrow", s.Rethrow != nil}, {"for_each", s.ForEach != ""},
		{"max_parallel", s.MaxParallel != nil}, {"fail_threshold", s.FailThreshold != nil},
		{"retry", s.Retry != nil},
	}
	if s.IsGroup() {
		v.errs = append(v.errs, errNotSupported(p+".type", `step type "group"`))
	}
	for _, f := range fields {
		if f.set {
			v.errs = append(v.errs, errNotSupported(p+"."+f.name, f.name))
		}
	}
	if s.IsGroup() && s.Plugin != "" {
		v.add(p+".plugin", "a group cannot also set plugin")
	}
}

func (v *validator) duration(path, s string) {
	if s == "" {
		return
	}
	d, err := time.ParseDuration(s)
	switch {
	case err != nil:
		v.add(path, "%q is not a duration such as \"30s\" or \"10m\"", s)
	case d <= 0:
		v.add(path, "must be positive")
	}
}

// expression compile-checks one CEL expression.
func (v *validator) expression(path, src string) {
	if _, err := v.env.Compile(src); err != nil {
		v.add(path, "%s", unwrapCompile(err))
	}
}

func unwrapCompile(err error) string {
	msg := err.Error()
	prefix := expr.ErrCompile.Error() + ": "
	if len(msg) > len(prefix) && msg[:len(prefix)] == prefix {
		return "invalid expression: " + msg[len(prefix):]
	}
	return msg
}

// templates compile-checks every ${{ }} expression found in strings of a
// JSON value, reporting the path of the offending string.
func (v *validator) templates(path string, val any) {
	switch t := val.(type) {
	case string:
		segs, err := expr.ParseTemplate(t)
		if err != nil {
			v.add(path, "%s", unwrapCompile(err))
			return
		}
		for _, seg := range segs {
			if seg.IsExpr {
				v.expression(path, seg.Expr)
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v.templates(path+"."+k, t[k])
		}
	case []any:
		for i, x := range t {
			v.templates(fmt.Sprintf("%s[%d]", path, i), x)
		}
	}
}
