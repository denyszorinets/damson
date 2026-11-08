package api

import (
	"net/http"
	"sort"
	"testing"

	"github.com/danielgtaylor/huma/v2"
)

// publicOps are the operations that need no token even with auth.require_token
// (with OIDC on all but list-secret-types need a login; see openOps).
// Every operation must be governed by routePolicies, guarded by the admin
// token, or listed here on purpose, so a new route cannot be left anonymous by
// accident (ADR-0060).
var publicOps = map[string]string{
	"list-agents":       "agent inventory, open before M8",
	"get-agent":         "agent inventory, open before M8",
	"list-pools":        "pool names, open before M8",
	"list-plugins":      "plugin schemas drive the forms",
	"get-plugin":        "plugin schemas drive the forms",
	"list-secret-types": "static list of secret types",
	"get-me":            "describes the request's own credentials; anonymous when login is off",
}

func TestEveryOperationHasAnAuthPolicy(t *testing.T) {
	doc := newHuma(http.NewServeMux(), deps{}, nil).OpenAPI()
	var unguarded []string
	known := map[string]bool{}
	for _, item := range doc.Paths {
		for _, op := range []*huma.Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if op == nil {
				continue
			}
			known[op.OperationID] = true
			_, governed := routePolicies[op.OperationID]
			_, public := publicOps[op.OperationID]
			if !governed && !public && len(op.Security) == 0 {
				unguarded = append(unguarded, op.OperationID)
			}
		}
	}
	sort.Strings(unguarded)
	if len(unguarded) > 0 {
		t.Errorf("operations with no auth policy (add them to routePolicies, guard them with requireAdmin, or list them in publicOps): %v", unguarded)
	}
	// The tables must not name operations that no longer exist.
	for id := range routePolicies {
		if !known[id] {
			t.Errorf("routePolicies names unknown operation %q", id)
		}
	}
	for id := range publicOps {
		if !known[id] {
			t.Errorf("publicOps names unknown operation %q", id)
		}
	}
}
