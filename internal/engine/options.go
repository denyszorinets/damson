package engine

import (
	"encoding/json"
	"fmt"
)

// WithOptionDefaults returns a deep copy of inputs with the `default` of every
// property declared in the job's spec.options schema filled in where the
// caller gave no value, recursing into nested object properties. It does not
// validate: the caller checks the result against spec.options (the
// controller uses pluginreg.ValidateJSONSchema, which bounds the schema and
// denies URL loading). Defaults are taken from the schema as written, so they
// hold secret references at most, never values (ARCHITECTURE section 9).
func WithOptionDefaults(job *Job, inputs map[string]any) (map[string]any, error) {
	out := map[string]any{}
	if len(inputs) > 0 {
		b, err := json.Marshal(inputs)
		if err != nil {
			return nil, fmt.Errorf("copying options: %w", err)
		}
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, fmt.Errorf("copying options: %w", err)
		}
	}
	applyDefaults(job.Spec.Options, out)
	return out, nil
}

// applyDefaults fills missing properties of obj from schema["properties"].
func applyDefaults(schema map[string]any, obj map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	for name, raw := range props {
		ps, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		cur, present := obj[name]
		if !present {
			if def, has := ps["default"]; has {
				obj[name] = cloneJSON(def)
			}
			continue
		}
		if child, ok := cur.(map[string]any); ok {
			applyDefaults(ps, child)
		}
	}
}

// cloneJSON deep-copies a decoded JSON value.
func cloneJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[k] = cloneJSON(x)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, x := range t {
			s[i] = cloneJSON(x)
		}
		return s
	default:
		return v
	}
}
