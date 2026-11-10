package plugintest

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaURL is the internal location generated schemas are compiled under.
const schemaURL = "mem://damson/schema.json"

// validateAgainst checks that doc (a JSON value) conforms to schema (any value
// that marshals to a JSON Schema, such as the manifest's *jsonschema.Schema).
func validateAgainst(schema any, doc []byte) error {
	raw, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("marshaling schema: %w", err)
	}
	sd, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("decoding schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaURL, sd); err != nil {
		return fmt.Errorf("adding schema: %w", err)
	}
	s, err := c.Compile(schemaURL)
	if err != nil {
		return fmt.Errorf("compiling schema: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return fmt.Errorf("decoding document: %w", err)
	}
	if err := s.Validate(inst); err != nil {
		return fmt.Errorf("%w", err)
	}
	return nil
}
