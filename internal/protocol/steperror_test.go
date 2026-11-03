package protocol

import (
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

// exit_code is optional on the wire: it round-trips, a message from an agent
// without it (protocol N-1) decodes with it unset, and a peer that does not
// know it (N-1) skips it.
func TestStepErrorExitCodeIsBackwardCompatible(t *testing.T) {
	code := 3
	b, err := msgpack.Marshal(StepError{Code: "exit_code", Message: "m", ExitCode: &code})
	if err != nil {
		t.Fatal(err)
	}
	var got StepError
	if err := msgpack.Unmarshal(b, &got); err != nil || got.ExitCode == nil || *got.ExitCode != 3 {
		t.Fatalf("round trip = %+v, %v", got, err)
	}

	// Absent: not on the wire at all.
	b, err = msgpack.Marshal(StepError{Code: "timeout", Message: "m"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := msgpack.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["exit_code"]; ok {
		t.Fatalf("exit_code encoded when absent: %v", m)
	}
	got = StepError{}
	if err := msgpack.Unmarshal(b, &got); err != nil || got.ExitCode != nil {
		t.Fatalf("N-1 message = %+v, %v", got, err)
	}

	// A peer at N-1 has no ExitCode field.
	type oldStepError struct {
		Code      string `msgpack:"code,omitempty"`
		Message   string `msgpack:"message"`
		Retryable bool   `msgpack:"retryable,omitempty"`
	}
	b, _ = msgpack.Marshal(StepError{Code: "exit_code", Message: "m", ExitCode: &code})
	var old oldStepError
	if err := msgpack.Unmarshal(b, &old); err != nil || old.Code != "exit_code" || old.Message != "m" {
		t.Fatalf("N-1 decode = %+v, %v", old, err)
	}
}
