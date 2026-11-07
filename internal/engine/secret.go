package engine

import (
	"fmt"
	"sort"
	"strings"
)

// SecretMarker is the schema annotation that marks a value as a secret
// reference (ARCHITECTURE sections 5.4 and 9). In spec.options a property so
// marked, and everything below it, accepts only secret:// references.
const SecretMarker = "x-damson-secret"

// SecretRefPrefix starts every secret reference.
const SecretRefPrefix = "secret://"

func isSecretRef(s string) bool {
	return strings.HasPrefix(s, SecretRefPrefix) && len(s) > len(SecretRefPrefix)
}

func marked(schema map[string]any) bool {
	b, _ := schema[SecretMarker].(bool)
	return b
}

// SecretOptionViolations returns the locations ("options.token",
// "options.keys[1]") of values in opts that sit under an x-damson-secret
// property of spec.options but are not secret:// references. It never returns
// or formats a value.
func SecretOptionViolations(schema map[string]any, opts map[string]any) []string {
	var out []string
	walkSecretValue(schema, false, opts, "options", &out)
	return out
}

func walkSecretValue(schema map[string]any, inSecret bool, v any, path string, out *[]string) {
	secret := inSecret || marked(schema)
	switch t := v.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		extra, _ := schema["additionalProperties"].(map[string]any)
		for k, child := range t {
			cs, _ := props[k].(map[string]any)
			if cs == nil {
				cs = extra
			}
			walkSecretValue(cs, secret, child, path+"."+k, out)
		}
	case []any:
		items, _ := schema["items"].(map[string]any)
		for i, child := range t {
			walkSecretValue(items, secret, child, fmt.Sprintf("%s[%d]", path, i), out)
		}
	case string:
		if secret && !isSecretRef(t) {
			*out = append(*out, path)
		}
	default:
		if secret {
			*out = append(*out, path)
		}
	}
}

// checkSecretSchema statically checks spec.options: below an x-damson-secret
// property only strings (or arrays and objects of them) make sense, and no
// default, const or enum may hold anything but a secret:// reference, since
// those literals would be stored and returned verbatim.
func (v *validator) checkSecretSchema(path string, schema map[string]any, inSecret bool) {
	secret := inSecret || marked(schema)
	if secret {
		switch t := schema["type"].(type) {
		case nil, string:
			if t != nil && t != "string" && t != "array" && t != "object" {
				v.add(path+".type", "a secret option must be a string (or an array or object of strings)")
			}
		default:
			v.add(path+".type", "a secret option must have a single type")
		}
		v.secretLiteral(path+".default", schema, "default")
		v.secretLiteral(path+".const", schema, "const")
		if enum, ok := schema["enum"].([]any); ok {
			for i, e := range enum {
				if !literalIsRefs(e) {
					v.add(fmt.Sprintf("%s.enum[%d]", path, i), "a secret option may only list secret:// references")
				}
			}
		}
	}
	props, _ := schema["properties"].(map[string]any)
	for name, raw := range props {
		if child, ok := raw.(map[string]any); ok {
			v.checkSecretSchema(path+".properties."+name, child, secret)
		}
	}
	for _, key := range []string{"items", "additionalProperties"} {
		if child, ok := schema[key].(map[string]any); ok {
			v.checkSecretSchema(path+"."+key, child, secret)
		}
	}
}

func (v *validator) secretLiteral(path string, schema map[string]any, key string) {
	if lit, ok := schema[key]; ok && !literalIsRefs(lit) {
		v.add(path, "a secret option may only default to a secret:// reference")
	}
}

// literalIsRefs reports whether every leaf of lit is a secret reference.
func literalIsRefs(lit any) bool {
	switch t := lit.(type) {
	case string:
		return isSecretRef(t)
	case []any:
		for _, x := range t {
			if !literalIsRefs(x) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, x := range t {
			if !literalIsRefs(x) {
				return false
			}
		}
		return true
	}
	return false
}

// SecretOptionKeys returns those of keys (top-level option names) whose schema
// in spec.options is, or contains, an x-damson-secret property. Pure. A key the
// schema does not name falls under additionalProperties.
func SecretOptionKeys(schema map[string]any, keys []string) []string {
	props, _ := schema["properties"].(map[string]any)
	extra, _ := schema["additionalProperties"].(map[string]any)
	var out []string
	for _, k := range keys {
		cs, _ := props[k].(map[string]any)
		if cs == nil {
			cs = extra
		}
		if containsSecret(cs) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func containsSecret(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	if marked(schema) {
		return true
	}
	for _, key := range []string{"items", "additionalProperties"} {
		if c, _ := schema[key].(map[string]any); containsSecret(c) {
			return true
		}
	}
	props, _ := schema["properties"].(map[string]any)
	for _, p := range props {
		if c, _ := p.(map[string]any); containsSecret(c) {
			return true
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		list, _ := schema[key].([]any)
		for _, p := range list {
			if c, _ := p.(map[string]any); containsSecret(c) {
				return true
			}
		}
	}
	return false
}
