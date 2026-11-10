package engine

import (
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"
)

// Schema returns the generated JSON Schema of the job spec (a Job document).
// It is derived from the Go types and their tags, never hand-written; the
// committed copy in testdata/engine/job-v1.schema.json is refreshed by
// `make gen`.
//
// Conventions follow pluginsdk.SchemaFor: a field is required unless its json
// tag has omitempty, and unknown keys are rejected.
func Schema() ([]byte, error) {
	r := &jsonschema.Reflector{ExpandedStruct: true}
	s := r.Reflect(&Job{})
	s.Title = "Damson job spec v1"
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling job schema: %w", err)
	}
	return append(b, '\n'), nil
}
