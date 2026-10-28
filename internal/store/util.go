package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// NormalizeTime returns t in UTC truncated to microseconds, the precision both
// backends store, so values round-trip identically.
func NormalizeTime(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// ValidateHello rejects hellos the store cannot attribute.
func ValidateHello(h AgentHello) error {
	if h.ControllerID == "" {
		return fmt.Errorf("hello for agent %q: empty controller id: %w", h.ID, ErrInvalid)
	}
	if h.PoolID == "" {
		return fmt.Errorf("hello for agent %q: empty pool id: %w", h.ID, ErrInvalid)
	}
	return nil
}

// NewID returns a new UUIDv7 string, the id format of every store-generated
// row. Ids are generated in Go, never by the database.
func NewID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generating id: %w", err)
	}
	return id.String(), nil
}

// EncodeJSON marshals v for a JSON column. JSON handling stays in Go, never
// in SQL.
func EncodeJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encoding json column: %w", err)
	}
	return b, nil
}

// EncodeStringMap marshals a string map, encoding nil as {}.
func EncodeStringMap(m map[string]string) ([]byte, error) {
	if m == nil {
		m = map[string]string{}
	}
	return EncodeJSON(m)
}

// ObjectOrEmpty returns raw, or {} when raw is empty.
func ObjectOrEmpty(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}

// Page size bounds for keyset-paginated lists.
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 500
)

// ClampLimit maps a requested page size into [1, MaxPageLimit]; <= 0 means the default.
func ClampLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultPageLimit
	case n > MaxPageLimit:
		return MaxPageLimit
	}
	return n
}

// DecodeJSON unmarshals a JSON column into v.
func DecodeJSON(b []byte, v any) error {
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("decoding json column: %w", err)
	}
	return nil
}

// JSONOrEmpty validates and compacts raw for a JSON text column, or returns
// {} when empty. Invalid JSON and the \u0000 escape (which Postgres cannot
// store in jsonb and tools cannot round-trip) are ErrInvalid.
func JSONOrEmpty(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return []byte("{}"), nil
	}
	if err := checkJSON("column value", raw); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, fmt.Errorf("column value is not valid json: %w", ErrInvalid)
	}
	return buf.Bytes(), nil
}

// checkJSON rejects non-empty raw that is invalid JSON or contains \u0000.
func checkJSON(what string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	if !json.Valid(raw) {
		return fmt.Errorf("%s is not valid json: %w", what, ErrInvalid)
	}
	if bytes.Contains(raw, []byte(`\u0000`)) {
		return fmt.Errorf("%s contains a \\u0000 escape: %w", what, ErrInvalid)
	}
	return nil
}
