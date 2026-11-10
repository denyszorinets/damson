package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

// CSRFHeader carries the CSRF token on cookie-authenticated writes.
const CSRFHeader = "X-CSRF-Token"

// sessionCookie and loginCookie name the cookies. The __Host- prefix makes a
// browser accept them only with Secure, Path=/ and no Domain; it is dropped for
// plain-http development (auth.cookie_insecure).
func (o *OIDC) sessionCookie() string { return o.cookieName("damson_session") }
func (o *OIDC) loginCookie() string   { return o.cookieName("damson_login") }

func (o *OIDC) cookieName(n string) string {
	if o.insecure {
		return n
	}
	return "__Host-" + n
}

func (o *OIDC) setCookie(w http.ResponseWriter, name, value string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: int(maxAge.Seconds()),
		HttpOnly: true, Secure: !o.insecure, SameSite: http.SameSiteLaxMode,
	})
}

func (o *OIDC) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: !o.insecure, SameSite: http.SameSiteLaxMode,
	})
}

// cookieValue reads one cookie from a raw Cookie header.
func cookieValue(header, name string) string {
	c, err := (&http.Request{Header: http.Header{"Cookie": {header}}}).Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// randToken is 32 random bytes in base64url.
func randToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// csrfToken is derived from the session cookie value, so it needs no storage
// and a different session has a different token.
func csrfToken(sessionToken string) string {
	m := hmac.New(sha256.New, []byte(sessionToken))
	m.Write([]byte("damson-csrf"))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// safeMethod is a method that does not change state.
func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// csrfOK checks a cookie-authenticated write: the CSRF header must match the
// session's token, and a browser must say the request is same-origin.
func csrfOK(method, host, origin, fetchSite, header, sessionToken string) bool {
	if safeMethod(method) {
		return true
	}
	if subtle.ConstantTimeCompare([]byte(header), []byte(csrfToken(sessionToken))) != 1 {
		return false
	}
	if fetchSite != "" && fetchSite != "same-origin" && fetchSite != "none" {
		return false
	}
	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != host {
			return false
		}
	}
	return true
}

var (
	errNoSession = errors.New("no session")
	errCSRF      = errors.New("csrf check failed")
)

// sessionRequest is what a session lookup needs from a request.
type sessionRequest struct {
	cookies, method, host, origin, fetchSite, csrf string
}

// session finds the session of a request's cookie. Validity is read from the
// database on every call, so a logout on any controller applies at once.
// Unsafe methods must also pass the CSRF check.
func (d deps) session(ctx context.Context, r sessionRequest) (store.Session, string, error) {
	tok := cookieValue(r.cookies, d.oidc.sessionCookie())
	if tok == "" {
		return store.Session{}, "", errNoSession
	}
	s, err := d.ds.FindSession(ctx, tokens.Hash(tok), d.now(), sessionIdle)
	if errors.Is(err, store.ErrNotFound) {
		return store.Session{}, "", errNoSession
	}
	if err != nil {
		return store.Session{}, "", err
	}
	if !csrfOK(r.method, r.host, r.origin, r.fetchSite, r.csrf, tok) {
		return store.Session{}, "", errCSRF
	}
	if now := d.now(); now.Sub(s.LastSeenAt) >= sessionTouch {
		_ = d.ds.TouchSession(ctx, s.ID, now) // best effort
	}
	return s, tok, nil
}

// safeReturnTo accepts only a path on this site, so a login cannot send the
// browser elsewhere. Anything else becomes "/".
func safeReturnTo(p string) string {
	if p == "" || len(p) > 512 || p[0] != '/' || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n\t") {
		return "/"
	}
	if u, err := url.Parse(p); err != nil || u.Host != "" || u.Scheme != "" {
		return "/"
	}
	return p
}
