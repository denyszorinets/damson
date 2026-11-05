package secrets

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// InventoryScope is the reserved scope of node credentials. Project scopes
// cannot start with "_", so it never collides with one.
const InventoryScope = "_inventory"

const (
	// RefPrefix starts every secret reference.
	RefPrefix  = "secret://"
	maxRefLen  = 512
	maxSegment = 8
)

var (
	segmentRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	nameRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	fieldRE   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// ErrBadRef is returned for a string that is not a valid secret reference.
var ErrBadRef = errors.New("invalid secret reference")

// Ref is a parsed secret://<scope>/<name>[#<field>] reference. The scope is
// every path segment but the last.
type Ref struct {
	Scope string
	Name  string
	Field string
}

// ParseRef validates s against the grammar of ARCHITECTURE 9.1. Errors name
// the problem, never echo more than the grammar needs.
func ParseRef(s string) (Ref, error) {
	if len(s) > maxRefLen {
		return Ref{}, fmt.Errorf("%w: longer than %d bytes", ErrBadRef, maxRefLen)
	}
	rest, ok := strings.CutPrefix(s, RefPrefix)
	if !ok {
		return Ref{}, fmt.Errorf("%w: missing %s prefix", ErrBadRef, RefPrefix)
	}
	path, field, hasField := strings.Cut(rest, "#")
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return Ref{}, fmt.Errorf("%w: want <scope>/<name>", ErrBadRef)
	}
	r := Ref{Scope: path[:i], Name: path[i+1:]}
	if err := ValidateScope(r.Scope); err != nil {
		return Ref{}, err
	}
	if err := ValidateName(r.Name); err != nil {
		return Ref{}, err
	}
	if hasField {
		if !fieldRE.MatchString(field) {
			return Ref{}, fmt.Errorf("%w: bad field name", ErrBadRef)
		}
		r.Field = field
	}
	return r, nil
}

// ValidateScope accepts _inventory alone or 1 to 8 project segments.
func ValidateScope(scope string) error {
	if scope == InventoryScope {
		return nil
	}
	segs := strings.Split(scope, "/")
	if len(segs) > maxSegment {
		return fmt.Errorf("%w: scope has more than %d segments", ErrBadRef, maxSegment)
	}
	for _, s := range segs {
		if !segmentRE.MatchString(s) {
			return fmt.Errorf("%w: bad scope segment", ErrBadRef)
		}
	}
	return nil
}

// ValidateName checks the last path segment.
func ValidateName(name string) error {
	if !nameRE.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("%w: bad name", ErrBadRef)
	}
	return nil
}

// Base is the ref without its field.
func (r Ref) Base() string { return RefPrefix + r.Scope + "/" + r.Name }

// String renders the ref, with "#field" when set.
func (r Ref) String() string {
	if r.Field == "" {
		return r.Base()
	}
	return r.Base() + "#" + r.Field
}
