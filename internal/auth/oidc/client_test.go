package oidc_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/denyszorinets/damson/internal/auth/oidc"
	"github.com/denyszorinets/damson/internal/auth/oidctest"
)

const redirect = "http://app.test/auth/callback"

func newClient(t *testing.T, iss *oidctest.Issuer) oidc.Client {
	t.Helper()
	c, err := oidc.New(oidc.Config{
		Issuer: iss.URL(), ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret, RedirectURL: redirect,
		Scopes: []string{"profile", "email", "groups"}, PostLogoutRedirectURL: "http://app.test/",
	})
	require.NoError(t, err)
	return c
}

// login walks the authorize endpoint like a browser and returns the code.
func login(t *testing.T, c oidc.Client, user, nonce, verifier string) string {
	t.Helper()
	u, err := c.AuthCodeURL(t.Context(), "state-1", nonce, verifier)
	require.NoError(t, err)
	parsed, err := url.Parse(u)
	require.NoError(t, err)
	q := parsed.Query()
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	assert.Equal(t, nonce, q.Get("nonce"))
	assert.Contains(t, q.Get("scope"), "openid")
	q.Set("login_hint", user)
	parsed.RawQuery = q.Encode()
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Get(parsed.String())
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "state-1", loc.Query().Get("state"))
	return loc.Query().Get("code")
}

func TestExchangeHappyPath(t *testing.T) {
	iss := oidctest.New()
	defer iss.Close()
	c := newClient(t, iss)
	v := oauth2.GenerateVerifier()
	code := login(t, c, oidctest.Bob.Sub, "n1", v)

	id, err := c.Exchange(t.Context(), code, v, "n1")
	require.NoError(t, err)
	assert.Equal(t, "bob-sub", id.Sub)
	assert.Equal(t, "bob@example.org", id.Email)
	assert.Equal(t, []any{"team-ops"}, id.Claims["groups"])

	// A code works once.
	_, err = c.Exchange(t.Context(), code, v, "n1")
	assert.ErrorIs(t, err, oidc.ErrInvalidToken)
}

func TestExchangeRejects(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(i *oidctest.Issuer)
		nonce  string // nonce sent to Exchange; default n1
		verify string // verifier sent to Exchange; empty means the right one
		reason string
	}{
		{name: "wrong nonce", nonce: "other", reason: "nonce"},
		{name: "wrong verifier", verify: oauth2.GenerateVerifier(), reason: "refused the code"},
		{name: "wrong audience", setup: func(i *oidctest.Issuer) { i.Mutate = func(c map[string]any) { c["aud"] = "someone-else" } }, reason: "audience"},
		{name: "wrong issuer", setup: func(i *oidctest.Issuer) { i.Mutate = func(c map[string]any) { c["iss"] = "http://evil.example" } }, reason: "issuer"},
		{name: "expired", setup: func(i *oidctest.Issuer) {
			i.Mutate = func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }
		}, reason: "expired"},
		{name: "alg none", setup: func(i *oidctest.Issuer) { i.Alg = "none" }, reason: ""},
		{name: "no id token", setup: func(i *oidctest.Issuer) { i.NoIDToken = true }, reason: "no id token"},
		{name: "no subject", setup: func(i *oidctest.Issuer) { i.Mutate = func(c map[string]any) { c["sub"] = "" } }, reason: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iss := oidctest.New()
			defer iss.Close()
			if tt.setup != nil {
				tt.setup(iss)
			}
			c := newClient(t, iss)
			v := oauth2.GenerateVerifier()
			code := login(t, c, oidctest.Alice.Sub, "n1", v)
			nonce, verifier := "n1", v
			if tt.nonce != "" {
				nonce = tt.nonce
			}
			if tt.verify != "" {
				verifier = tt.verify
			}
			_, err := c.Exchange(t.Context(), code, verifier, nonce)
			require.ErrorIs(t, err, oidc.ErrInvalidToken)
			if tt.reason != "" {
				assert.Contains(t, err.Error(), tt.reason)
			}
		})
	}
}

func TestDiscoveryIsRetriedAfterFailure(t *testing.T) {
	iss := oidctest.New()
	defer iss.Close()
	iss.Down.Store(true)
	c := newClient(t, iss) // a controller must start while the provider is down
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := c.AuthCodeURL(ctx, "s", "n", oauth2.GenerateVerifier())
	require.Error(t, err)
	iss.Down.Store(false)
	_, err = c.AuthCodeURL(ctx, "s", "n", oauth2.GenerateVerifier())
	assert.NoError(t, err, "discovery must be retried after a failure")
}

func TestEndSessionURL(t *testing.T) {
	iss := oidctest.New()
	defer iss.Close()
	u, err := newClient(t, iss).EndSessionURL(t.Context())
	require.NoError(t, err)
	parsed, err := url.Parse(u)
	require.NoError(t, err)
	assert.Equal(t, iss.URL()+"/logout", parsed.Scheme+"://"+parsed.Host+parsed.Path)
	assert.Equal(t, oidctest.ClientID, parsed.Query().Get("client_id"))
	assert.Equal(t, "http://app.test/", parsed.Query().Get("post_logout_redirect_uri"))
}
