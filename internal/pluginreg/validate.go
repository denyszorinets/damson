package pluginreg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// FieldError is one precise validation failure. Path is "$" for the input
// itself or "a.b[0].c"; Message never contains an input value.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationErrors is the list of failures; it implements error.
type ValidationErrors []FieldError

func (v ValidationErrors) Error() string {
	parts := make([]string, len(v))
	for i, e := range v {
		parts[i] = e.Path + ": " + e.Message
	}
	return "invalid plugin input: " + strings.Join(parts, "; ")
}

type compiledSchema struct {
	schema *jsonschema.Schema
	err    error
}

// ValidateInput checks resolved step input against m.InputSchema. A returned
// ValidationErrors means the input is invalid; any other error means the
// manifest schema itself is unusable. A secret:// reference is an ordinary
// string to the schema; messages describe the failure without echoing
// values, so a secret placed in the wrong field is never leaked.
func ValidateInput(m pluginsdk.Manifest, input json.RawMessage) error {
	sch, err := compileInput(m)
	if err != nil {
		return err
	}
	return validate(sch, input)
}

// ValidateInput is the registry's cached form: each distinct manifest
// (by checksum) is compiled once.
func (r *Registry) ValidateInput(m pluginsdk.Manifest, input json.RawMessage) error {
	sum, err := m.Checksum()
	if err != nil {
		return fmt.Errorf("checksumming manifest %q: %w", m.Name, err)
	}
	r.vmu.Lock()
	c, ok := r.validators[sum]
	if !ok {
		c = &compiledSchema{}
		c.schema, c.err = compileInput(m)
		r.validators[sum] = c
	}
	r.vmu.Unlock()
	if c.err != nil {
		return c.err
	}
	return validate(c.schema, input)
}

// schemaURL is a placeholder resource id; the loader below never fetches it.
const schemaURL = "damson://plugin/input.json"

// compileInput compiles InputSchema with a loader that denies every URL
// scheme, so a $ref to file: or http: fails to compile instead of doing I/O.
func compileInput(m pluginsdk.Manifest) (*jsonschema.Schema, error) {
	if m.InputSchema == nil {
		return nil, fmt.Errorf("plugin %q has no input schema", m.Name)
	}
	raw, err := json.Marshal(m.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("encoding input schema of %q: %w", m.Name, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("loading input schema of %q: %w", m.Name, err)
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(jsonschema.SchemeURLLoader{})
	if err := c.AddResource(schemaURL, doc); err != nil {
		return nil, fmt.Errorf("adding input schema of %q: %w", m.Name, err)
	}
	sch, err := c.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("compiling input schema of %q: %w", m.Name, err)
	}
	return sch, nil
}

func validate(sch *jsonschema.Schema, input json.RawMessage) error {
	if len(bytes.TrimSpace(input)) == 0 {
		input = json.RawMessage("null")
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(input))
	if err != nil {
		return ValidationErrors{{Path: "$", Message: "input is not valid JSON"}}
	}
	verr := sch.Validate(inst)
	if verr == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(verr, &ve) {
		return fmt.Errorf("validating plugin input: %w", verr)
	}
	return fieldErrors(ve)
}

// fieldErrors flattens the leaf causes, de-duplicated.
func fieldErrors(ve *jsonschema.ValidationError) ValidationErrors {
	pr := message.NewPrinter(language.English)
	var out ValidationErrors
	seen := map[FieldError]bool{}
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			fe := FieldError{Path: pointerToPath(e.InstanceLocation), Message: safeMessage(e.ErrorKind, pr)}
			if !seen[fe] {
				seen[fe] = true
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

// safeMessage renders a failure without the offending value. The library
// echoes the instance for a few keywords (pattern, format, content*); those
// are rewritten here. Keywords that print schema values (enum, const) or
// property names are safe.
func safeMessage(k jsonschema.ErrorKind, pr *message.Printer) string {
	switch t := k.(type) {
	case *kind.Pattern:
		return "does not match pattern " + strconv.Quote(t.Want)
	case *kind.Format:
		return "is not a valid " + t.Want
	case *kind.ContentEncoding:
		return "is not valid " + t.Want + " content"
	case *kind.ContentMediaType:
		return "is not valid " + t.Want + " content"
	case *kind.ContentSchema:
		return "content does not match its schema"
	}
	return k.LocalizedString(pr)
}

// pointerToPath turns JSON Pointer tokens into "auth.keys[1].name".
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
