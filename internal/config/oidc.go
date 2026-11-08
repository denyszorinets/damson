package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// OIDCEnabled reports whether browser login is configured.
func (c Config) OIDCEnabled() bool { return c.Auth.OIDC.Issuer != "" }

// ResolveOIDCSecret returns the client secret: auth.oidc.client_secret, else the
// content of auth.oidc.client_secret_file with surrounding whitespace removed.
func (c Config) ResolveOIDCSecret() (string, error) {
	o := c.Auth.OIDC
	if o.ClientSecret != "" || o.ClientSecretFile == "" {
		return o.ClientSecret, nil
	}
	b, err := os.ReadFile(o.ClientSecretFile)
	if err != nil {
		return "", fmt.Errorf("auth.oidc.client_secret_file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

func (c Config) validateOIDC(add func(key, format string, args ...any)) {
	o := c.Auth.OIDC
	if o.Issuer == "" {
		return
	}
	// Over plain http anyone on the path could swap the signing keys and forge
	// a login for any user, so http is for development only.
	plainOK := c.Auth.CookieInsecure || c.Controller.InsecureDev
	okScheme := func(u *url.URL) bool { return u.Scheme == "https" || (u.Scheme == "http" && plainOK) }
	if u, err := url.Parse(o.Issuer); err != nil || u.Host == "" || !okScheme(u) {
		add("auth.oidc.issuer", "must be an https URL with a host (http only with auth.cookie_insecure or controller.insecure_dev); got %q", o.Issuer)
	}
	if o.ClientID == "" {
		add("auth.oidc.client_id", "is required with auth.oidc.issuer")
	}
	if o.ClientSecret == "" && o.ClientSecretFile == "" {
		add("auth.oidc.client_secret", "is required with auth.oidc.issuer (or set auth.oidc.client_secret_file)")
	} else if _, err := c.ResolveOIDCSecret(); err != nil {
		add("auth.oidc.client_secret_file", "%v", err)
	}
	if u, err := url.Parse(o.RedirectURL); err != nil || u.Host == "" || !okScheme(u) || !strings.HasSuffix(u.Path, "/auth/callback") {
		add("auth.oidc.redirect_url", "must be this controller's public https URL ending in /auth/callback; got %q", o.RedirectURL)
	}
	if o.GroupsClaim == "" {
		add("auth.oidc.groups_claim", "must not be empty")
	}
	if o.PostLogoutRedirectURL != "" {
		if u, err := url.Parse(o.PostLogoutRedirectURL); err != nil || u.Host == "" {
			add("auth.oidc.post_logout_redirect_url", "must be an absolute URL; got %q", o.PostLogoutRedirectURL)
		}
	}
}
