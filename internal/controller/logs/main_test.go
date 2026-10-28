package logs

import (
	"fmt"
	"os"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	code := m.Run()
	stopPostgres()
	if code == 0 {
		if err := goleak.Find(); err != nil {
			fmt.Fprintf(os.Stderr, "goleak: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}
