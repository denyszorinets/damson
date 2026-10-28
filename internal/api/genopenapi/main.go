// Command genopenapi writes the OpenAPI document of the controller API to the
// file named by its only argument. It is run by make gen.
package main

import (
	"fmt"
	"os"

	"github.com/denyszorinets/damson/internal/api"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: genopenapi <output.json>")
		os.Exit(2)
	}
	b, err := api.OpenAPIJSON()
	if err == nil {
		err = os.WriteFile(os.Args[1], b, 0o644) //nolint:gosec // generated, world-readable spec
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "genopenapi:", err)
		os.Exit(1)
	}
}
