package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

const schemaURL = "damson:job-v1.schema.json"

// Parse decodes and validates a job spec document. On invalid input the error
// is a ValidationErrors with precise paths. Plugin names are not checked
// here: whether a plugin exists and what its input schema is belongs to the
// caller (the plugin registry).
//
// Parse compiles the job schema on every call; use (*Engine).Parse to reuse a
// schema compiled once by New.
func Parse(data []byte) (*Job, error) {
	compiled, err := compileJobSchema()
	if err != nil {
		return nil, err
	}
	return parseWith(compiled, data)
}

// Parse is the package-level Parse using the schema compiled by New.
func (e *Engine) Parse(data []byte) (*Job, error) { return parseWith(e.jobSchema, data) }

func parseWith(compiled *jsonschema.Schema, data []byte) (*Job, error) {
	if errs, err := validateSchema(compiled, data); err != nil {
		return nil, err
	} else if len(errs) > 0 {
		return nil, errs
	}
	var j Job
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&j); err != nil {
		return nil, fmt.Errorf("decoding job spec: %w", err)
	}
	if errs := Validate(&j); len(errs) > 0 {
		return nil, errs
	}
	return &j, nil
}

// newSchemaCompiler returns a compiler that never performs I/O: every URL
// scheme is denied, so a $ref in a user-supplied schema (file:, http:, ...)
// fails to compile instead of reading the disk or the network. The engine is
// pure (hard rule 1).
func newSchemaCompiler() *jsonschema.Compiler {
	c := jsonschema.NewCompiler()
	c.UseLoader(jsonschema.SchemeURLLoader{})
	return c
}

// compileJobSchema compiles the generated job schema.
func compileJobSchema() (*jsonschema.Schema, error) {
	schemaBytes, err := Schema()
	if err != nil {
		return nil, err
	}
	sch, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return nil, fmt.Errorf("loading job schema: %w", err)
	}
	c := newSchemaCompiler()
	if err := c.AddResource(schemaURL, sch); err != nil {
		return nil, fmt.Errorf("adding job schema: %w", err)
	}
	compiled, err := c.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("compiling job schema: %w", err)
	}
	return compiled, nil
}

// validateSchema checks data against the compiled job schema. A non-nil error
// means the schema itself is broken; invalid input yields ValidationErrors.
func validateSchema(compiled *jsonschema.Schema, data []byte) (ValidationErrors, error) {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, ValidationErrors{{Path: "$", Message: "not valid JSON: " + err.Error()}}
	}
	verr := compiled.Validate(inst)
	if verr == nil {
		return nil, nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(verr, &ve) {
		return nil, fmt.Errorf("validating job spec: %w", verr)
	}
	return schemaFieldErrors(ve), nil
}

// schemaFieldErrors flattens the leaf causes of a schema validation error.
func schemaFieldErrors(ve *jsonschema.ValidationError) ValidationErrors {
	pr := message.NewPrinter(language.English)
	var out ValidationErrors
	seen := map[string]bool{}
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			fe := FieldError{Path: pointerToPath(e.InstanceLocation), Message: e.ErrorKind.LocalizedString(pr)}
			if k := fe.Path + "\x00" + fe.Message; !seen[k] {
				seen[k] = true
				out = append(out, fe)
			}
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	return out
}

// pointerToPath turns JSON Pointer tokens into "spec.steps[1].id".
func pointerToPath(tokens []string) string {
	var b strings.Builder
	for _, t := range tokens {
		if _, err := strconv.Atoi(t); err == nil {
			b.WriteString("[" + t + "]")
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(t)
	}
	if b.Len() == 0 {
		return "$"
	}
	return b.String()
}
