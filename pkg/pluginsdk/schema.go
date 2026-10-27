package pluginsdk

import (
	"fmt"
	"reflect"

	"github.com/invopop/jsonschema"
)

// SchemaFor generates the JSON Schema for the Go struct type T from its json
// and jsonschema tags. Schemas are never hand-written.
//
// Conventions of the generated schema: a field is required unless its json tag
// has omitempty (or omitzero); every object has additionalProperties:false, so
// unknown keys are rejected. For a plugin's InputSchema the controller
// validates the RESOLVED input, after ${{ ... }} expressions are evaluated,
// not the saved job spec. The generator is github.com/invopop/jsonschema,
// pinned in go.mod; a version bump that changes output is a checksum change
// and is caught by the golden test.
func SchemaFor[T any]() (*jsonschema.Schema, error) {
	t := reflect.TypeFor[T]()
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("schema type %s must be a struct", t)
	}
	r := &jsonschema.Reflector{ExpandedStruct: true}
	return r.ReflectFromType(t), nil
}

// MustSchemaFor is SchemaFor for use in Manifest constructors. It panics only
// for non-struct T, which is a programming error caught by any test.
func MustSchemaFor[T any]() *jsonschema.Schema {
	s, err := SchemaFor[T]()
	if err != nil {
		panic(err)
	}
	return s
}
