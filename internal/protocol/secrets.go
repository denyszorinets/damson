package protocol

import (
	"fmt"
	"log/slog"
)

const redacted = "[REDACTED]"

// SecretBundle is the {ref -> fields} map sent inside step.execute
// (ARCHITECTURE.md section 9.3). It is the only place secret values travel on
// the wire. Every textual rendering (fmt verbs, slog, encoding/json) is
// redacted so that logging a message cannot leak a value. The msgpack wire
// encoding is the one deliberate exception.
//
// JSON redaction is one-way: MarshalJSON writes a marker and the type cannot
// be unmarshaled from JSON. Redaction applies only to the SecretBundle type
// itself: converting it to its underlying map[string]map[string]string, or
// ranging over it, bypasses every guard here, so never do either in code that
// logs or serializes.
type SecretBundle map[string]map[string]string

// String implements fmt.Stringer without exposing values.
func (b SecretBundle) String() string {
	return fmt.Sprintf("%s(%d refs)", redacted, len(b))
}

// GoString covers the %#v verb.
func (b SecretBundle) GoString() string { return b.String() }

// Format covers every fmt verb, including %v, %+v and %#v on a value that
// contains the bundle as a field.
func (b SecretBundle) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(b.String()))
}

// LogValue implements slog.LogValuer.
func (b SecretBundle) LogValue() slog.Value {
	return slog.StringValue(b.String())
}

// MarshalJSON writes a redaction marker instead of the values.
func (b SecretBundle) MarshalJSON() ([]byte, error) {
	return []byte(`"` + redacted + `"`), nil
}
