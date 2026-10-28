package pluginreg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Limits that keep a hostile agent or caller from costing unbounded memory or
// CPU.
const (
	// MaxSchemaBytes caps the JSON size of a manifest input schema.
	MaxSchemaBytes = 256 << 10
	// MaxSchemaDepth caps the nesting depth of a manifest input schema.
	MaxSchemaDepth = 64
	// MaxInputBytes caps the resolved step input handed to validation.
	MaxInputBytes = 1 << 20
	// maxCompiledSchemas bounds the compiled-schema cache; it is cleared when
	// full (simple, and hostile churn only costs recompilation).
	maxCompiledSchemas = 256
)

// Errors for oversized material. Use errors.Is.
var (
	ErrSchemaTooLarge = errors.New("input schema exceeds the size or depth limit")
	ErrInputTooLarge  = errors.New("plugin input exceeds the size limit")
)

// FieldError is one precise validation failure. Path is "$" for the input
// itself or "a.b[0].c"; Message never contains an input value or an input
// property name, only schema-side names and limits.
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

// schemaEntry compiles once, outside the cache lock.
type schemaEntry struct {
	once   sync.Once
	schema *jsonschema.Schema
	err    error
}

// ValidateInput checks resolved step input against m.InputSchema without a
// cache. A returned ValidationErrors means the input is invalid; any other
// error means the manifest schema is unusable or the input too large. A
// secret:// reference is an ordinary string to the schema; messages never
// echo values, so a secret placed in the wrong field is not leaked.
func ValidateInput(m pluginsdk.Manifest, input json.RawMessage) error {
	if len(input) > MaxInputBytes {
		return ErrInputTooLarge
	}
	sch, err := compileInput(m)
	if err != nil {
		return err
	}
	return validate(sch, input)
}

// ValidateInput is the cached form for a manifest the caller holds; it
// computes the manifest checksum on every call. Dispatch should prefer
// ValidateResolved.
func (r *Registry) ValidateInput(m pluginsdk.Manifest, input json.RawMessage) error {
	sum, err := m.Checksum()
	if err != nil {
		return fmt.Errorf("checksumming manifest %q: %w", m.Name, err)
	}
	return r.validateWith(m, sum, input)
}

// ValidateResolved validates input against the manifest that Resolve chose,
// reusing its checksum. This is the dispatch-time check.
func (r *Registry) ValidateResolved(res Resolution, input json.RawMessage) error {
	return r.validateWith(res.Manifest, res.Checksum, input)
}

func (r *Registry) validateWith(m pluginsdk.Manifest, sum string, input json.RawMessage) error {
	if len(input) > MaxInputBytes {
		return ErrInputTooLarge
	}
	sch, err := r.schemaFor(m, sum)
	if err != nil {
		return err
	}
	return validate(sch, input)
}

// schemaFor returns the compiled input schema for a manifest checksum. The
// cache is bounded and clear-on-full; failures are never cached, so a hostile
// manifest cannot grow memory with negative entries. Compilation runs outside
// the lock, once per entry.
func (r *Registry) schemaFor(m pluginsdk.Manifest, sum string) (*jsonschema.Schema, error) {
	r.vmu.Lock()
	e := r.validators[sum]
	if e == nil {
		if len(r.validators) >= r.maxEntryCap {
			clear(r.validators)
		}
		e = &schemaEntry{}
		r.validators[sum] = e
	}
	r.vmu.Unlock()
	e.once.Do(func() { e.schema, e.err = compileInput(m) })
	if e.err != nil {
		r.vmu.Lock()
		if r.validators[sum] == e {
			delete(r.validators, sum)
		}
		r.vmu.Unlock()
		return nil, e.err
	}
	return e.schema, nil
}

// schemaURL is a placeholder resource id; the loader below never fetches it.
const schemaURL = "damson://plugin/input.json"

// compileInput compiles InputSchema after bounding its size and depth, with a
// loader that denies every URL scheme, so a $ref to file: or http: fails to
// compile instead of doing I/O.
func compileInput(m pluginsdk.Manifest) (*jsonschema.Schema, error) {
	if m.InputSchema == nil {
		return nil, fmt.Errorf("plugin %q has no input schema", m.Name)
	}
	raw, err := json.Marshal(m.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("encoding input schema of %q: %w", m.Name, err)
	}
	if len(raw) > MaxSchemaBytes || jsonDepth(raw) > MaxSchemaDepth {
		return nil, fmt.Errorf("plugin %q: %w", m.Name, ErrSchemaTooLarge)
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
	// Reference cycles surface lazily, at validation time; probe once so an
	// unusable schema is refused when it is first seen.
	if err := cycleFault(sch.Validate(map[string]any{})); err != nil {
		return nil, fmt.Errorf("input schema of %q is unusable: %w", m.Name, err)
	}
	return sch, nil
}

// cycleFault reports a reference cycle among the causes of a validation
// error; that is a fault of the schema, not of the input.
func cycleFault(verr error) error {
	var ve *jsonschema.ValidationError
	if !errors.As(verr, &ve) {
		return nil
	}
	var walk func(*jsonschema.ValidationError) bool
	walk = func(e *jsonschema.ValidationError) bool {
		if _, ok := e.ErrorKind.(*kind.RefCycle); ok {
			return true
		}
		for _, c := range e.Causes {
			if walk(c) {
				return true
			}
		}
		return false
	}
	if walk(ve) {
		return errors.New("reference cycle")
	}
	return nil
}

// jsonDepth is the maximum bracket nesting of a JSON document, ignoring
// brackets inside strings.
func jsonDepth(raw []byte) int {
	depth, max := 0, 0
	inStr, esc := false, false
	for _, c := range raw {
		switch {
		case esc:
			esc = false
		case inStr:
			if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '{' || c == '[':
			if depth++; depth > max {
				max = depth
			}
		case c == '}' || c == ']':
			depth--
		}
	}
	return max
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
	if err := cycleFault(verr); err != nil {
		return fmt.Errorf("validating plugin input: %w", err)
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

// safeMessage renders a failure without the offending value or any property
// name taken from the input. The library echoes those for several keywords;
// they are rewritten here. Keywords that print only schema-side values (enum,
// const wants, required, dependentRequired, lengths and counts) are safe.
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
	case *kind.AdditionalProperties: // also reported for unevaluatedProperties
		return strconv.Itoa(len(t.Properties)) + " additional or unevaluated properties not allowed"
	case *kind.PropertyNames:
		return "a property name is not allowed"
	case *kind.Minimum:
		return "must be at least " + t.Want.RatString()
	case *kind.Maximum:
		return "must be at most " + t.Want.RatString()
	case *kind.ExclusiveMinimum:
		return "must be greater than " + t.Want.RatString()
	case *kind.ExclusiveMaximum:
		return "must be less than " + t.Want.RatString()
	case *kind.MultipleOf:
		return "must be a multiple of " + t.Want.RatString()
	}
	return k.LocalizedString(pr)
}

// pointerToPath turns JSON Pointer tokens into "auth.keys[1].name". Tokens
// are property names from the instance location, which exists in the input
// only when the schema declared it (or an additionalProperties schema did).
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
