package store

import (
	"testing"
	"time"
)

func TestTimeoutMillis(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want int64
	}{
		{0, 0}, {-time.Second, 0}, {time.Nanosecond, 1}, {500 * time.Microsecond, 1},
		{time.Millisecond, 1}, {1500 * time.Millisecond, 1500}, {1500*time.Millisecond + 1, 1501}, {time.Hour, 3600000},
	}
	for _, tc := range tests {
		if got := TimeoutMillis(tc.in); got != tc.want {
			t.Errorf("TimeoutMillis(%v) = %d; want %d", tc.in, got, tc.want)
		}
	}
}

func TestStepErrorTextRoundTrip(t *testing.T) {
	tests := []struct{ kind, msg string }{
		{"timeout", "step timed out"},
		{"exit_code", `exit "2"`},
		{"x", ""},
	}
	for _, tc := range tests {
		kind, msg := SplitStepError(StepErrorText(tc.kind, tc.msg))
		if kind != tc.kind || msg != tc.msg {
			t.Errorf("round trip of (%q, %q) = (%q, %q)", tc.kind, tc.msg, kind, msg)
		}
	}
	// The exit code rides along and does not disturb the kind and message.
	code := 3
	if kind, msg := SplitStepError(StepErrorTextExit("exit_code", "exit 3", &code)); kind != "exit_code" || msg != "exit 3" {
		t.Errorf("with exit code = (%q, %q)", kind, msg)
	}
	if got := StepErrorTextExit("exit_code", "m", &code); got != `{"kind":"exit_code","message":"m","exit_code":3}` {
		t.Errorf("encoding = %s", got)
	}
	if got := StepErrorText("timeout", "m"); got != `{"kind":"timeout","message":"m"}` {
		t.Errorf("no exit code must be omitted: %s", got)
	}
	// No kind: plain text, and plain text (even JSON without a kind) is not parsed.
	for _, text := range []string{"boom", "", `{"message":"x"}`, `{"kind":"","message":"x"}`} {
		if kind, msg := SplitStepError(text); kind != "" || msg != text {
			t.Errorf("SplitStepError(%q) = (%q, %q); want plain text", text, kind, msg)
		}
	}
}

func TestErrorKindOf(t *testing.T) {
	tests := []struct {
		name string
		ex   Execution
		want string
	}{
		{"stored kind wins", Execution{State: ExecFailed, Error: StepErrorText("exit_code", "x")}, "exit_code"},
		{"timed out", Execution{State: ExecTimedOut, Error: "x"}, ErrKindTimeout},
		{"lost", Execution{State: ExecLost, Error: "lease expired"}, ErrKindLost},
		{"canceled", Execution{State: ExecCanceled, Error: "run cancelled"}, ErrKindCancelled},
		{"failed plain", Execution{State: ExecFailed, Error: "boom"}, ErrKindError},
		{"succeeded", Execution{State: ExecSucceeded}, ""},
	}
	for _, tc := range tests {
		if got := ErrorKindOf(tc.ex); got != tc.want {
			t.Errorf("%s: ErrorKindOf = %q; want %q", tc.name, got, tc.want)
		}
	}
}
