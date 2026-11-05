package api

import (
	"errors"
	"net/http"

	"github.com/denyszorinets/damson/internal/inventory"
)

const typeSourceConflict = "urn:damson:problem:inventory-source-conflict"

// nodeProblem maps the inventory errors.
func nodeProblem(err error) (Problem, bool) {
	if errors.Is(err, inventory.ErrSourceConflict) {
		return Problem{Type: typeSourceConflict, Title: "Conflict", Status: http.StatusConflict,
			Detail: "the node is owned by another source; use force to edit it (the next sync overwrites the edit)"}, true
	}
	return Problem{}, false
}
