package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// OpenAPIJSON renders the OpenAPI 3.1 document of the typed API without a
// store. It is the source of web/openapi.json (make gen) and is compared
// against the committed copy in tests.
func OpenAPIJSON() ([]byte, error) {
	b, err := json.MarshalIndent(newHuma(http.NewServeMux(), nil).OpenAPI(), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding openapi document: %w", err)
	}
	return append(b, '\n'), nil
}
