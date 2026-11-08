package api

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/denyszorinets/damson/internal/auth/oidc"
	"github.com/denyszorinets/damson/internal/expr"
)

// Lifetimes of logins and sessions (ADR-0063).
const (
	loginTTL        = 10 * time.Minute
	sessionLifetime = 12 * time.Hour // absolute
	sessionIdle     = SessionIdle
	sessionTouch    = time.Minute // last_seen_at moves at most this often
)

// SessionIdle is how long a session may go unused before it ends.
const SessionIdle = 2 * time.Hour

// OIDCSettings configures browser login.
type OIDCSettings struct {
	Client oidc.Client
	// GroupsClaim names the id token claim holding the user's groups.
	GroupsClaim string
	// AdminRule is a CEL rule over claims and user that makes a user an instance
	// admin; empty means nobody is (the recovery token still is).
	AdminRule string
	// CookieInsecure drops the Secure flag and the __Host- prefix of cookies.
	CookieInsecure bool
}

// OIDC is the login configuration of the API. Its zero value is not usable;
// build it with NewOIDC. A nil *OIDC means login is off.
type OIDC struct {
	client      oidc.Client
	env         *expr.Env
	admin       *expr.Program
	groupsClaim string
	insecure    bool
	log         *slog.Logger
}

// NewOIDC checks the settings and compiles the admin rule, so a bad rule stops
// the controller at startup.
func NewOIDC(s OIDCSettings, log *slog.Logger) (*OIDC, error) {
	if s.Client == nil {
		return nil, fmt.Errorf("oidc: a client is required")
	}
	env, err := expr.NewAccessEnv(expr.Limits{})
	if err != nil {
		return nil, err
	}
	o := &OIDC{client: s.Client, env: env, groupsClaim: s.GroupsClaim, insecure: s.CookieInsecure, log: log}
	if o.groupsClaim == "" {
		o.groupsClaim = "groups"
	}
	if o.log == nil {
		o.log = slog.Default()
	}
	if s.AdminRule != "" {
		if o.admin, err = env.CompileRule(s.AdminRule); err != nil {
			return nil, fmt.Errorf("auth.oidc.admin_rule: %w", err)
		}
	}
	return o, nil
}

// identity is the user as access rules see them.
func (o *OIDC) identity(id oidc.Identity) expr.Identity {
	var groups []string
	if list, ok := id.Claims[o.groupsClaim].([]any); ok {
		for _, g := range list {
			if s, ok := g.(string); ok {
				groups = append(groups, s)
			}
		}
	}
	return expr.Identity{Sub: id.Sub, Email: id.Email, Name: id.Name, Groups: groups, Claims: id.Claims}
}

// isInstanceAdmin evaluates the admin rule. A rule that fails to evaluate (for
// example a missing claim) grants nothing; the log names the user, not claims.
func (o *OIDC) isInstanceAdmin(id oidc.Identity) bool {
	if o.admin == nil {
		return false
	}
	ok, err := o.env.EvalRule(o.admin, o.identity(id))
	if err != nil {
		o.log.Warn("admin rule did not evaluate; no admin role granted", "sub", id.Sub, "error", err)
		return false
	}
	return ok
}
