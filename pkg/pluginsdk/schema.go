package pluginsdk

import (
	"fmt"
	"reflect"

	"github.com/invopop/jsonschema"
)

// SchemaFor generates the JSON Schema for the Go struct type T from its json
// and jsonschema tags. Schemas are never hand-written.
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
