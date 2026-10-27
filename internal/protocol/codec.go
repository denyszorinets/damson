package protocol

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/vmihailenco/msgpack/v5"
)

// MaxFrameSize is the largest frame, in bytes, accepted or produced
// (ARCHITECTURE.md section 7.1).
const MaxFrameSize = 4 << 20

// JSONRPCVersion is the value of the jsonrpc member.
const JSONRPCVersion = "2.0"

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Frame errors.
var (
	ErrFrameTooLarge = errors.New("frame exceeds 4 MiB")
	ErrBadFrame      = errors.New("malformed frame")
)

// RPCError is the JSON-RPC error object. It is also a Go error.
type RPCError struct {
	Code    int                `msgpack:"code" json:"code"`
	Message string             `msgpack:"message" json:"message"`
	Data    msgpack.RawMessage `msgpack:"data,omitempty" json:"-"`
}

// Error implements error.
func (e *RPCError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// Message is one JSON-RPC 2.0 object. A request has Method and ID, a
// notification has Method and no ID, a response has no Method and either
// Result or Error. ID, Params and Result stay encoded until needed.
type Message struct {
	JSONRPC string             `msgpack:"jsonrpc" json:"jsonrpc"`
	ID      msgpack.RawMessage `msgpack:"id,omitempty" json:"-"`
	Method  string             `msgpack:"method,omitempty" json:"method,omitempty"`
	Params  msgpack.RawMessage `msgpack:"params,omitempty" json:"-"`
	Result  msgpack.RawMessage `msgpack:"result,omitempty" json:"-"`
	Error   *RPCError          `msgpack:"error,omitempty" json:"error,omitempty"`
}

// IsRequest reports whether m expects a response.
func (m *Message) IsRequest() bool { return m.Method != "" && len(m.ID) > 0 }

// IsNotification reports whether m is a request without an id.
func (m *Message) IsNotification() bool { return m.Method != "" && len(m.ID) == 0 }

// IsResponse reports whether m answers a request.
func (m *Message) IsResponse() bool { return m.Method == "" }

// Encode marshals v as MessagePack using the json struct tags where no
// msgpack tag exists.
func Encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	enc.SetCustomStructTag("json")
	enc.UseCompactInts(true)
	enc.SetSortMapKeys(true)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encoding msgpack: %w", err)
	}
	return buf.Bytes(), nil
}

// Decode unmarshals MessagePack into v. Unknown fields are ignored so a
// newer peer can add fields (protocol N-1 compatibility).
func Decode(b []byte, v any) error {
	dec := msgpack.NewDecoder(bytes.NewReader(b))
	dec.SetCustomStructTag("json")
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decoding msgpack: %w", err)
	}
	return nil
}

// EncodeMessage serializes m into one frame, enforcing MaxFrameSize.
func EncodeMessage(m *Message) ([]byte, error) {
	if m.JSONRPC == "" {
		m.JSONRPC = JSONRPCVersion
	}
	b, err := Encode(m)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxFrameSize {
		return nil, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, len(b))
	}
	return b, nil
}

// DecodeMessage parses one frame, enforcing MaxFrameSize and the envelope
// rules of JSON-RPC 2.0.
func DecodeMessage(b []byte) (*Message, error) {
	if len(b) > MaxFrameSize {
		return nil, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, len(b))
	}
	var m Message
	if err := Decode(b, &m); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadFrame, err)
	}
	switch {
	case m.JSONRPC != JSONRPCVersion:
		return nil, fmt.Errorf("%w: jsonrpc must be %q", ErrBadFrame, JSONRPCVersion)
	case m.IsResponse() && len(m.ID) == 0 && m.Error == nil:
		return nil, fmt.Errorf("%w: response has no id", ErrBadFrame)
	}
	return &m, nil
}

// raw encodes v for a Params or Result member. A nil v becomes MessagePack nil
// so that a result member is always present.
func raw(v any) (msgpack.RawMessage, error) {
	return Encode(v)
}

// NewRequest builds a request with a numeric id.
func NewRequest(id uint64, method string, params any) (*Message, error) {
	m, err := NewNotification(method, params)
	if err != nil {
		return nil, err
	}
	if m.ID, err = Encode(id); err != nil {
		return nil, err
	}
	return m, nil
}

// NewNotification builds a request without an id.
func NewNotification(method string, params any) (*Message, error) {
	m := &Message{JSONRPC: JSONRPCVersion, Method: method}
	if params != nil {
		p, err := raw(params)
		if err != nil {
			return nil, fmt.Errorf("encoding params of %s: %w", method, err)
		}
		m.Params = p
	}
	return m, nil
}

// NewResult builds a success response to the request with the given raw id.
func NewResult(id msgpack.RawMessage, result any) (*Message, error) {
	r, err := raw(result)
	if err != nil {
		return nil, fmt.Errorf("encoding result: %w", err)
	}
	return &Message{JSONRPC: JSONRPCVersion, ID: id, Result: r}, nil
}

// NewError builds an error response. id may be empty when the request id is
// unknown, in which case it is sent as null.
func NewError(id msgpack.RawMessage, e *RPCError) *Message {
	if len(id) == 0 {
		id = msgpack.RawMessage{0xc0}
	}
	return &Message{JSONRPC: JSONRPCVersion, ID: id, Error: e}
}

// Params is the lazily decoded params member of an incoming request.
type Params struct{ raw msgpack.RawMessage }

// Decode unmarshals the params into v. A failure is an *RPCError with
// CodeInvalidParams, so a handler can return it unchanged.
func (p Params) Decode(v any) error {
	if len(p.raw) == 0 {
		return &RPCError{Code: CodeInvalidParams, Message: "missing params"}
	}
	if err := Decode(p.raw, v); err != nil {
		return &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	return nil
}

// Raw returns the encoded params.
func (p Params) Raw() []byte { return p.raw }
