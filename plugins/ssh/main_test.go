package ssh_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package when a plugin or test server goroutine outlives
// the tests.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
