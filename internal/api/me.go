package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// Me describes who the request is.
type Me struct {
	Kind          string   `json:"kind" enum:"anonymous,admin_token,service_account,user" doc:"How the request was authenticated."`
	Actor         string   `json:"actor,omitempty" doc:"The audit actor: user:<sub>, sa:<id> or admin-token:<hash prefix>."`
	Sub           string   `json:"sub,omitempty"`
	Email         string   `json:"email,omitempty"`
	Name          string   `json:"name,omitempty"`
	Groups        []string `json:"groups,omitempty"`
	InstanceAdmin bool     `json:"instance_admin" doc:"True for the admin token and for users the admin rule selected."`
	CSRFToken     string   `json:"csrf_token,omitempty" doc:"Send as X-CSRF-Token on writes made with the session cookie."`
}

func registerMe(api huma.API, d deps) {
	huma.Register(api, huma.Operation{
		OperationID: meOperation, Method: http.MethodGet, Path: "/me", Summary: "Who am I", Tags: []string{"auth"},
		Description: "Describes the credentials of the request. With login configured a request without credentials gets 401.",
		Errors:      []int{http.StatusUnauthorized, http.StatusServiceUnavailable},
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body Me }, error) {
		p, ok := PrincipalFrom(ctx)
		me := Me{Kind: "anonymous"}
		switch {
		case !ok:
		case p.Session != nil:
			me = Me{
				Kind: "user", Actor: p.Actor, Sub: p.Session.Sub, Email: p.Session.Email, Name: p.Session.Name,
				InstanceAdmin: p.InstanceAdmin, CSRFToken: p.CSRF, Groups: sessionGroups(d, p.Session.Claims),
			}
		case p.Token != nil:
			me = Me{Kind: "service_account", Actor: p.Actor}
		default:
			me = Me{Kind: "admin_token", Actor: p.Actor, InstanceAdmin: p.InstanceAdmin}
		}
		return &struct{ Body Me }{me}, nil
	})
}

// sessionGroups reads the groups claim out of a session's claims snapshot.
func sessionGroups(d deps, claims json.RawMessage) []string {
	var m map[string]any
	if json.Unmarshal(claims, &m) != nil {
		return nil
	}
	var out []string
	if list, ok := m[d.oidc.groupsClaim].([]any); ok {
		for _, g := range list {
			if s, ok := g.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
