package secrets

import (
	"errors"
	"strings"
)

// ErrDenied means a job may not use a secret ref.
var ErrDenied = errors.New("secret ref not allowed for this job")

// Authorize applies the scope rule of ARCHITECTURE 9.1: a job may use secrets
// of its own project and of its ancestors (project is a path). The reserved
// _inventory scope is allowed only as the credential of a node the step
// targets (fromNode), never as a literal in step input.
func Authorize(project string, r Ref, fromNode bool) error {
	if r.Scope == InventoryScope {
		if fromNode {
			return nil
		}
		return ErrDenied
	}
	if r.Scope == project || strings.HasPrefix(project, r.Scope+"/") {
		return nil
	}
	return ErrDenied
}
