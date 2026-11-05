package api

import (
	"errors"
	"net/http"

	"github.com/denyszorinets/damson/internal/inventory"
)

const (
	typeSourceConflict = "urn:damson:problem:inventory-source-conflict"
	typeSyncConflict   = "urn:damson:problem:inventory-sync-conflict"
)

// nodeProblem maps the inventory errors.
func nodeProblem(err error) (Problem, bool) {
	var sc *syncConflictError
	if errors.As(err, &sc) {
		p := Problem{Type: typeSyncConflict, Title: "Conflict", Status: http.StatusConflict,
			Detail: "the sync would clash with existing node names; nothing was written"}
		for _, c := range sc.conflicts {
			p.Errors = append(p.Errors, ProblemDetail{Message: "held by " + c.OwnedBy, Location: "node." + c.Name})
		}
		return p, true
	}
	if errors.Is(err, inventory.ErrSourceConflict) {
		return Problem{Type: typeSourceConflict, Title: "Conflict", Status: http.StatusConflict,
			Detail: "the node is owned by another source; use force to edit it (the next sync overwrites the edit)"}, true
	}
	return Problem{}, false
}
