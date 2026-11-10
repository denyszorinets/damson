package secrets

import (
	"errors"
	"fmt"
	"sort"
	"unicode/utf8"
)

// Size limits of one secret version.
const (
	MaxFieldBytes = 64 << 10
	MaxTotalBytes = 256 << 10
	MaxFields     = 32
)

// Validation errors; messages name fields, never values.
var (
	ErrBadFields = errors.New("invalid secret fields")
	ErrTooLarge  = errors.New("secret too large")
	ErrBadType   = errors.New("unknown secret type")
)

// FieldSpec describes one field of a secret type (ARCHITECTURE 9.1).
type FieldSpec struct {
	Name      string `json:"name"`
	Required  bool   `json:"required"`
	Multiline bool   `json:"multiline"`
}

// Type is a secret type: a named set of fields. A nil Fields means any field
// matching the field grammar (generic).
type Type struct {
	Name   string      `json:"name"`
	Fields []FieldSpec `json:"fields,omitempty"`
}

var types = []Type{
	{Name: "generic"},
	{Name: "password", Fields: []FieldSpec{{Name: "username"}, {Name: "password", Required: true}}},
	{Name: "ssh_key", Fields: []FieldSpec{
		{Name: "username"},
		{Name: "private_key", Required: true, Multiline: true},
		{Name: "passphrase"},
	}},
	{Name: "token", Fields: []FieldSpec{{Name: "token", Required: true}}},
}

// Types returns the supported types.
func Types() []Type { return append([]Type(nil), types...) }

// LookupType returns the type called name.
func LookupType(name string) (Type, error) {
	for _, t := range types {
		if t.Name == name {
			return t, nil
		}
	}
	return Type{}, fmt.Errorf("%w %q", ErrBadType, name)
}

// ValidateFields checks fields against the type and the size limits. It never
// includes a value in an error.
func (t Type) ValidateFields(fields map[string]string) error {
	if len(fields) == 0 || len(fields) > MaxFields {
		return fmt.Errorf("%w: need 1 to %d fields", ErrBadFields, MaxFields)
	}
	total := 0
	for _, name := range sortedKeys(fields) {
		if !fieldRE.MatchString(name) {
			return fmt.Errorf("%w: bad field name %q", ErrBadFields, truncate(name, 64))
		}
		if len(fields[name]) > MaxFieldBytes {
			return fmt.Errorf("%w: field %q over %d bytes", ErrTooLarge, truncate(name, 64), MaxFieldBytes)
		}
		if !utf8.ValidString(fields[name]) {
			return fmt.Errorf("%w: field %q is not valid UTF-8", ErrBadFields, truncate(name, 64))
		}
		total += len(fields[name])
		if t.Fields != nil && !t.has(name) {
			return fmt.Errorf("%w: type %s has no field %q", ErrBadFields, t.Name, truncate(name, 64))
		}
	}
	if total > MaxTotalBytes {
		return fmt.Errorf("%w: over %d bytes in total", ErrTooLarge, MaxTotalBytes)
	}
	for _, f := range t.Fields {
		if _, ok := fields[f.Name]; f.Required && !ok {
			return fmt.Errorf("%w: type %s requires field %q", ErrBadFields, t.Name, f.Name)
		}
	}
	return nil
}

func (t Type) has(name string) bool {
	for _, f := range t.Fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

// truncate caps a client-supplied name before it is echoed in an error.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
