package cli

import (
	"fmt"
	"log/slog"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/auth/oidc"
	"github.com/denyszorinets/damson/internal/config"
)

// newOIDC builds the login configuration, or nil when auth.oidc.issuer is
// unset. It does no network I/O: the provider is discovered on first use. A bad
// admin rule fails here, at startup.
func newOIDC(cfg config.Config, log *slog.Logger) (*api.OIDC, error) {
	if !cfg.OIDCEnabled() {
		return nil, nil
	}
	secret, err := cfg.ResolveOIDCSecret()
	if err != nil {
		return nil, err
	}
	o := cfg.Auth.OIDC
	client, err := oidc.New(oidc.Config{
		Issuer: o.Issuer, ClientID: o.ClientID, ClientSecret: secret, RedirectURL: o.RedirectURL,
		Scopes: o.Scopes, PostLogoutRedirectURL: o.PostLogoutRedirectURL,
	})
	if err != nil {
		return nil, fmt.Errorf("configuring oidc: %w", err)
	}
	a, err := api.NewOIDC(api.OIDCSettings{
		Client: client, GroupsClaim: o.GroupsClaim, AdminRule: o.AdminRule, CookieInsecure: cfg.Auth.CookieInsecure,
	}, log)
	if err != nil {
		return nil, err
	}
	if cfg.Auth.CookieInsecure {
		log.Warn("auth.cookie_insecure is on: session cookies are sent over plain http (development only)")
	}
	log.Info("oidc login enabled; anonymous access to the job and run API is off", "issuer", o.Issuer)
	return a, nil
}
