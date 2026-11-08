package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/denyszorinets/damson/internal/auth/oidc"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

const (
	typeUnauthorized = "urn:damson:problem:unauthorized"
	typeForbidden    = "urn:damson:problem:forbidden"
	typeBadGateway   = "urn:damson:problem:bad-gateway"
)

// authHandlers serve /auth/login, /auth/callback and /auth/logout (ADR-0063).
type authHandlers struct {
	d deps
	// limit bounds /auth/login: each call writes a row, and it needs no login.
	limit *rateLimiter
}

// Login starts per controller: 20 a second, bursts of 40.
const (
	loginRate  = 20
	loginBurst = 40
)

func problem(w http.ResponseWriter, r *http.Request, status int, typ, detail string) {
	writeProblem(w, r, Problem{Type: typ, Title: http.StatusText(status), Status: status, Detail: detail})
}

// login starts the code flow. The state, nonce and PKCE verifier are random
// and stored (hashed where they are credentials) for the callback, which may
// reach any controller. A cookie binds the login to this browser.
func (h authHandlers) login(w http.ResponseWriter, r *http.Request) {
	o := h.d.oidc
	if ok, retry := h.limit.allow("login", h.d.now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		problem(w, r, http.StatusTooManyRequests, typeRateLimited, "too many logins; try again shortly")
		return
	}
	state, err1 := randToken()
	binding, err2 := randToken()
	nonce, err3 := randToken()
	verifier, err4 := randToken()
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		WriteError(w, r, err)
		return
	}
	authURL, err := o.client.AuthCodeURL(r.Context(), state, nonce, verifier)
	if err != nil {
		loggerFrom(r).Warn("oidc provider unavailable", "error", err)
		problem(w, r, http.StatusBadGateway, typeBadGateway, "the identity provider is not available; try again")
		return
	}
	err = h.d.ds.CreateOIDCLogin(r.Context(), store.OIDCLogin{
		StateHash: tokens.Hash(state), BindingHash: tokens.Hash(binding), Nonce: nonce, Verifier: verifier,
		ReturnTo: safeReturnTo(r.URL.Query().Get("return_to")), ExpiresAt: h.d.now().Add(loginTTL),
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	o.setCookie(w, o.loginCookie(), binding, loginTTL)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, authURL, http.StatusFound)
}

// callback finishes the flow. The login row is consumed first, so whatever
// else fails the state cannot be tried again. It must be presented with the
// cookie set by the same browser's login: a callback link forged by an attacker
// carries the attacker's state but not the victim's cookie.
func (h authHandlers) callback(w http.ResponseWriter, r *http.Request) {
	o := h.d.oidc
	q := r.URL.Query()
	state, code := q.Get("state"), q.Get("code")
	if state == "" {
		problem(w, r, http.StatusUnauthorized, typeUnauthorized, "the login is not valid; start again")
		return
	}
	login, err := h.d.ds.ConsumeOIDCLogin(r.Context(), tokens.Hash(state), h.d.now())
	if errors.Is(err, store.ErrNotFound) {
		problem(w, r, http.StatusUnauthorized, typeUnauthorized, "the login is not valid or has expired; start again")
		return
	}
	if err != nil {
		WriteError(w, r, err)
		return
	}
	binding := cookieValue(r.Header.Get("Cookie"), o.loginCookie())
	o.clearCookie(w, o.loginCookie())
	if binding == "" || subtle.ConstantTimeCompare([]byte(tokens.Hash(binding)), []byte(login.BindingHash)) != 1 {
		problem(w, r, http.StatusUnauthorized, typeUnauthorized, "the login was not started in this browser; start again")
		return
	}
	if q.Get("error") != "" || code == "" {
		problem(w, r, http.StatusUnauthorized, typeUnauthorized, "the identity provider refused the login")
		return
	}
	id, err := o.client.Exchange(r.Context(), code, login.Verifier, login.Nonce)
	switch {
	case errors.Is(err, oidc.ErrInvalidToken):
		loggerFrom(r).Warn("oidc login rejected", "reason", err.Error())
		problem(w, r, http.StatusUnauthorized, typeUnauthorized, "the identity provider's answer was not accepted")
		return
	case err != nil:
		loggerFrom(r).Warn("oidc provider unavailable", "error", err)
		problem(w, r, http.StatusBadGateway, typeBadGateway, "the identity provider is not available; try again")
		return
	}
	token, err := h.createSession(r, id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	// A new login replaces the browser's previous session.
	if old := cookieValue(r.Header.Get("Cookie"), o.sessionCookie()); old != "" {
		_ = h.d.ds.DeleteSession(r.Context(), tokens.Hash(old))
	}
	o.setCookie(w, o.sessionCookie(), token, sessionLifetime)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, login.ReturnTo, http.StatusFound)
}

// createSession stores a new session for id and returns its cookie value. A
// fresh token is issued on every login.
func (h authHandlers) createSession(r *http.Request, id oidc.Identity) (string, error) {
	token, err := randToken()
	if err != nil {
		return "", err
	}
	sid, err := store.NewID()
	if err != nil {
		return "", err
	}
	claims, err := snapshotClaims(id.Claims, h.d.oidc.groupsClaim)
	if err != nil {
		return "", err
	}
	now := h.d.now()
	err = h.d.ds.CreateSession(r.Context(), store.NewSession{
		Session: store.Session{
			ID: sid, Sub: id.Sub, Email: id.Email, Name: id.Name, Claims: claims,
			InstanceAdmin: h.d.oidc.isInstanceAdmin(id), ExpiresAt: now.Add(sessionLifetime),
		},
		TokenHash: tokens.Hash(token),
	}, now)
	return token, err
}

// snapshotClaims is the claims JSON a session keeps. Past the store's limit it
// keeps only the identity claims and the groups.
func snapshotClaims(claims map[string]any, groupsClaim string) (json.RawMessage, error) {
	b, err := json.Marshal(claims)
	if err != nil {
		return nil, err
	}
	if len(b) <= store.MaxSessionClaimsBytes {
		return b, nil
	}
	small := map[string]any{}
	for _, k := range []string{"sub", "email", "name", groupsClaim} {
		if v, ok := claims[k]; ok {
			small[k] = v
		}
	}
	return json.Marshal(small)
}

// logout ends the session and returns the provider's logout URL for the
// browser to visit. It is a POST with the CSRF token, so another site cannot
// log a user out.
func (h authHandlers) logout(w http.ResponseWriter, r *http.Request) {
	o := h.d.oidc
	tok := cookieValue(r.Header.Get("Cookie"), o.sessionCookie())
	if tok == "" {
		problem(w, r, http.StatusUnauthorized, typeUnauthorized, "not logged in")
		return
	}
	if !csrfOK(r.Method, r.Host, r.Header.Get("Origin"), r.Header.Get("Sec-Fetch-Site"), r.Header.Get(CSRFHeader), tok) {
		problem(w, r, http.StatusForbidden, typeForbidden, "the CSRF check failed")
		return
	}
	if err := h.d.ds.DeleteSession(r.Context(), tokens.Hash(tok)); err != nil {
		WriteError(w, r, err)
		return
	}
	o.clearCookie(w, o.sessionCookie())
	out := struct {
		EndSessionURL string `json:"end_session_url,omitempty"`
	}{}
	if u, err := o.client.EndSessionURL(r.Context()); err != nil {
		loggerFrom(r).Warn("oidc end session url unavailable", "error", err)
	} else if _, perr := url.Parse(u); perr == nil {
		out.EndSessionURL = u
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}
