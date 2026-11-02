package store

import (
	"encoding/json"
	"time"
)

// TimeoutMillis converts a step timeout to the whole milliseconds stored in
// executions.timeout_ms, rounding up so a sub-millisecond timeout never
// becomes "none".
func TimeoutMillis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d + time.Millisecond - 1) / time.Millisecond)
}

// Error kinds of a finished attempt, as the engine's error.kind sees them.
const (
	ErrKindError     = "error"
	ErrKindTimeout   = "timeout"
	ErrKindLost      = "lost"
	ErrKindCancelled = "cancelled"
)

type stepErrorJSON struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// StepErrorText encodes a step error as {"kind","message"}, the shape the
// engine writes for FailStep and reads back into error.kind. An empty kind
// leaves the message as plain text.
func StepErrorText(kind, message string) string {
	if kind == "" {
		return message
	}
	b, err := json.Marshal(stepErrorJSON{Kind: kind, Message: message})
	if err != nil { // two strings always marshal
		return message
	}
	return string(b)
}

// SplitStepError reads execution or step error text: JSON with a kind gives
// (kind, message); anything else (rows written before kinds were stored, or
// text from the lease reaper) is plain text with an empty kind.
func SplitStepError(text string) (kind, message string) {
	var e stepErrorJSON
	if json.Unmarshal([]byte(text), &e) == nil && e.Kind != "" {
		return e.Kind, e.Message
	}
	return "", text
}

// ErrorKindOf is the kind of a finished attempt's error: the stored kind, or
// one derived from the final state for plain text. Empty when it has no error.
func ErrorKindOf(ex Execution) string {
	if kind, _ := SplitStepError(ex.Error); kind != "" {
		return kind
	}
	switch ex.State {
	case ExecTimedOut:
		return ErrKindTimeout
	case ExecLost:
		return ErrKindLost
	case ExecCanceled:
		return ErrKindCancelled
	case ExecFailed:
		return ErrKindError
	}
	return ""
}
