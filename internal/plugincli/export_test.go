package plugincli

import "time"

// SetGraceForTest shortens the cancellation grace period; call the result to restore it.
func SetGraceForTest(d time.Duration) func() {
	old := graceAfterCancel
	graceAfterCancel = d
	return func() { graceAfterCancel = old }
}
