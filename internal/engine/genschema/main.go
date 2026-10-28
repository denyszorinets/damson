// Command genschema writes the generated job spec JSON Schema to a file.
// Usage: go run ./internal/engine/genschema <output-path>
package main

import (
	"fmt"
	"os"

	"github.com/denyszorinets/damson/internal/engine"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: genschema <output-path>")
		os.Exit(2)
	}
	b, err := engine.Schema()
	if err == nil {
		err = os.WriteFile(os.Args[1], b, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "genschema:", err)
		os.Exit(1)
	}
}
