// Package oidc is the controller's OpenID Connect client: the authorization code
// flow with PKCE against one provider (ARCHITECTURE 12.2, ADR-0063). The
// provider is discovered lazily and again after a failure, so a controller
// starts while the identity provider is down.
package oidc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Identity is a verified user: the subject and the raw claims of the id token.
type Identity struct {
	Sub    string
	Email  string
	Name   string
	Claims map[string]any
}

// Client is what the login handlers need from a provider.
type Client interface {
	// AuthCodeURL is the provider URL to send the browser to.
	AuthCodeURL(ctx context.Context, state, nonce, verifier string) (string, error)
	// Exchange trades the code for tokens and verifies the id token: signature,
	// issuer, audience, expiry and nonce. Claims come from the id token only.
	Exchange(ctx context.Context, code, verifier, nonce string) (Identity, error)
	// EndSessionURL is the provider's logout URL, or "" if it has none.
	EndSessionURL(ctx context.Context) (string, error)
}

// ErrInvalidToken marks a code or id token the provider's answer does not
// justify trusting. Its message never quotes token content.
var ErrInvalidToken = errors.New("oidc: invalid token")

// Config configures a provider client.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
	// PostLogoutRedirectURL is sent to the provider's end session endpoint.
	PostLogoutRedirectURL string
	// HTTPClient is used for discovery, keys and the token endpoint; nil means a
	// client with a 10 s timeout.
	HTTPClient *http.Client
	// Now is the clock id tokens are checked against; nil means time.Now.
	Now func() time.Time
}

type provider struct {
	p        *gooidc.Provider
	verifier *gooidc.IDTokenVerifier
	oauth    oauth2.Config
	endSess  string
}

// New returns a client. It does no network I/O.
func New(cfg Config) (Client, error) {
	switch {
	case cfg.Issuer == "" || cfg.ClientID == "" || cfg.RedirectURL == "":
		return nil, errors.New("oidc: an issuer, a client id and a redirect URL are required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &client{cfg: cfg}, nil
}

type client struct {
	cfg Config
	mu  sync.Mutex
	prv *provider
}

// get returns the discovered provider, discovering it on first use and after
// every failure.
func (c *client) get(ctx context.Context) (*provider, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.prv != nil {
		return c.prv, nil
	}
	dctx := gooidc.ClientContext(ctx, c.cfg.HTTPClient)
	p, err := gooidc.NewProvider(dctx, c.cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discovering oidc provider: %w", err)
	}
	var meta struct {
		EndSession string   `json:"end_session_endpoint"`
		Algs       []string `json:"id_token_signing_alg_values_supported"`
	}
	if err := p.Claims(&meta); err != nil {
		return nil, fmt.Errorf("reading oidc provider metadata: %w", err)
	}
	// Only asymmetric algorithms: never "none" and never an HMAC keyed by the
	// client secret.
	var algs []string
	for _, a := range meta.Algs {
		if strings.HasPrefix(a, "RS") || strings.HasPrefix(a, "PS") || strings.HasPrefix(a, "ES") || a == "EdDSA" {
			algs = append(algs, a)
		}
	}
	if len(algs) == 0 {
		algs = []string{gooidc.RS256}
	}
	scopes := slices.Clone(c.cfg.Scopes)
	if !slices.Contains(scopes, gooidc.ScopeOpenID) {
		scopes = append([]string{gooidc.ScopeOpenID}, scopes...)
	}
	c.prv = &provider{
		p: p,
		verifier: p.VerifierContext(dctx, &gooidc.Config{
			ClientID: c.cfg.ClientID, SupportedSigningAlgs: algs, Now: c.cfg.Now,
		}),
		oauth: oauth2.Config{
			ClientID: c.cfg.ClientID, ClientSecret: c.cfg.ClientSecret, Endpoint: p.Endpoint(),
			RedirectURL: c.cfg.RedirectURL, Scopes: scopes,
		},
		endSess: meta.EndSession,
	}
	return c.prv, nil
}

func (c *client) AuthCodeURL(ctx context.Context, state, nonce, verifier string) (string, error) {
	p, err := c.get(ctx)
	if err != nil {
		return "", err
	}
	return p.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), gooidc.Nonce(nonce)), nil
}

func (c *client) Exchange(ctx context.Context, code, verifier, nonce string) (Identity, error) {
	p, err := c.get(ctx)
	if err != nil {
		return Identity{}, err
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.cfg.HTTPClient)
	tok, err := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Identity{}, fmt.Errorf("%w: the token endpoint refused the code", ErrInvalidToken)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Identity{}, fmt.Errorf("%w: no id token", ErrInvalidToken)
	}
	idt, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %s", ErrInvalidToken, verifyReason(err))
	}
	if idt.Nonce == "" || idt.Nonce != nonce {
		return Identity{}, fmt.Errorf("%w: nonce mismatch", ErrInvalidToken)
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil || idt.Subject == "" {
		return Identity{}, fmt.Errorf("%w: unreadable claims", ErrInvalidToken)
	}
	id := Identity{Sub: idt.Subject, Claims: claims}
	id.Email, _ = claims["email"].(string)
	id.Name, _ = claims["name"].(string)
	return id, nil
}

// verifyReason keeps the library's reason (expired, wrong audience, bad
// signature) but not any token text it might echo.
func verifyReason(err error) string {
	var exp *gooidc.TokenExpiredError
	switch {
	case errors.As(err, &exp):
		return "id token expired"
	case strings.Contains(err.Error(), "audience"):
		return "wrong audience"
	case strings.Contains(err.Error(), "issuer") || strings.Contains(err.Error(), "different provider"):
		return "wrong issuer"
	case strings.Contains(err.Error(), "signature") || strings.Contains(err.Error(), "algorithm"):
		return "bad signature"
	}
	return "verification failed"
}

func (c *client) EndSessionURL(ctx context.Context) (string, error) {
	p, err := c.get(ctx)
	if err != nil {
		return "", err
	}
	if p.endSess == "" {
		return "", nil
	}
	u, err := url.Parse(p.endSess)
	if err != nil {
		return "", fmt.Errorf("oidc end_session_endpoint: %w", err)
	}
	q := u.Query()
	q.Set("client_id", c.cfg.ClientID)
	if c.cfg.PostLogoutRedirectURL != "" {
		q.Set("post_logout_redirect_uri", c.cfg.PostLogoutRedirectURL)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
