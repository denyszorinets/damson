// Package oidctest is a small OpenID Connect provider for tests: discovery, a
// JWKS, an authorize endpoint that approves at once as the user named by
// login_hint, a token endpoint that checks PKCE and the client secret, and an
// end session endpoint. It is a real HTTP server because the code exchange
// happens server to server.
package oidctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// User is a person the provider can log in.
type User struct {
	Sub    string
	Email  string
	Name   string
	Groups []string
}

// Default users, as in the development Keycloak realm.
var (
	Alice = User{Sub: "alice-sub", Email: "alice@example.org", Name: "Alice", Groups: []string{"damson-admins"}}
	Bob   = User{Sub: "bob-sub", Email: "bob@example.org", Name: "Bob", Groups: []string{"team-ops"}}
	Carol = User{Sub: "carol-sub", Email: "carol@example.org", Name: "Carol", Groups: []string{"team-viewers"}}
)

// ClientID and ClientSecret are what the provider accepts.
const (
	ClientID     = "damson"
	ClientSecret = "damson-secret"
)

// Issuer is a running fake provider.
type Issuer struct {
	srv *httptest.Server
	key *rsa.PrivateKey

	// Now is the clock of iat and exp; nil means time.Now.
	Now func() time.Time
	// Mutate, if set, may change the id token claims before signing, to
	// produce a wrong audience, issuer, nonce or expiry.
	Mutate func(claims map[string]any)
	// Alg overrides the signing algorithm: "none" produces an unsigned token.
	Alg string
	// Down makes every endpoint answer 503, to test discovery retries.
	Down atomic.Bool
	// NoIDToken makes the token endpoint omit the id token.
	NoIDToken bool

	// defaultSub is who logs in when the authorize request names nobody. A
	// browser test sets it with /_login-as?sub=... before it clicks login.
	defaultSub atomic.Value // string

	mu    sync.Mutex
	users map[string]User
	codes map[string]grant
}

type grant struct {
	user      User
	nonce     string
	challenge string
	redirect  string
}

// New starts a provider with the default users on a random port. Close stops it.
func New() *Issuer { return start("") }

// NewAt is New on a fixed address such as "127.0.0.1:4191", for tests that
// configure the issuer URL before the provider runs (the browser tests).
func NewAt(addr string) *Issuer { return start(addr) }

func start(addr string) *Issuer {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	i := &Issuer{key: key, users: map[string]User{}, codes: map[string]grant{}}
	for _, u := range []User{Alice, Bob, Carol} {
		i.users[u.Sub] = u
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", i.discovery)
	mux.HandleFunc("/jwks", i.jwks)
	mux.HandleFunc("/authorize", i.authorize)
	mux.HandleFunc("/token", i.token)
	mux.HandleFunc("/logout", func(w http.ResponseWriter, r *http.Request) {
		if back := r.URL.Query().Get("post_logout_redirect_uri"); back != "" {
			http.Redirect(w, r, back, http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/_login-as", i.loginAs)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if i.Down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		mux.ServeHTTP(w, r)
	})
	if addr == "" {
		i.srv = httptest.NewServer(handler)
		return i
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		panic(err)
	}
	i.srv = httptest.NewUnstartedServer(handler)
	i.srv.Listener = ln
	i.srv.Start()
	return i
}

// URL is the issuer URL.
func (i *Issuer) URL() string { return i.srv.URL }

// Close stops the provider.
func (i *Issuer) Close() { i.srv.Close() }

// AddUser makes another user available to login_hint.
func (i *Issuer) AddUser(u User) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.users[u.Sub] = u
}

func (i *Issuer) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (i *Issuer) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer": i.URL(), "authorization_endpoint": i.URL() + "/authorize", "token_endpoint": i.URL() + "/token",
		"jwks_uri": i.URL() + "/jwks", "end_session_endpoint": i.URL() + "/logout",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

// loginAs makes sub the user of the next logins (test control endpoint).
func (i *Issuer) loginAs(w http.ResponseWriter, r *http.Request) {
	sub := r.URL.Query().Get("sub")
	i.mu.Lock()
	_, ok := i.users[sub]
	i.mu.Unlock()
	if !ok {
		http.Error(w, "unknown user", http.StatusBadRequest)
		return
	}
	i.defaultSub.Store(sub)
	w.WriteHeader(http.StatusNoContent)
}

func (i *Issuer) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := i.key.PublicKey
	writeJSON(w, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
		"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

func (i *Issuer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect := q.Get("redirect_uri")
	if q.Get("client_id") != ClientID || q.Get("response_type") != "code" || redirect == "" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	hint := q.Get("login_hint")
	if hint == "" {
		hint, _ = i.defaultSub.Load().(string)
	}
	if hint == "" {
		hint = Alice.Sub
	}
	i.mu.Lock()
	u, ok := i.users[hint]
	code := randomString()
	if ok {
		i.codes[code] = grant{user: u, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: redirect}
	}
	i.mu.Unlock()
	if !ok {
		http.Error(w, "unknown user", http.StatusBadRequest)
		return
	}
	back, err := url.Parse(redirect)
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	bq := back.Query()
	bq.Set("code", code)
	bq.Set("state", q.Get("state"))
	back.RawQuery = bq.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (i *Issuer) token(w http.ResponseWriter, r *http.Request) {
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	}
	if id != ClientID || secret != ClientSecret {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	i.mu.Lock()
	g, found := i.codes[r.PostFormValue("code")]
	delete(i.codes, r.PostFormValue("code")) // a code works once
	i.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
	if !found || b64(sum[:]) != g.challenge || r.PostFormValue("redirect_uri") != g.redirect {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	out := map[string]any{"access_token": randomString(), "token_type": "Bearer", "expires_in": 300}
	if !i.NoIDToken {
		out["id_token"] = i.idToken(g)
	}
	writeJSON(w, out)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (i *Issuer) idToken(g grant) string {
	now := i.now()
	groups := make([]any, len(g.user.Groups))
	for n, s := range g.user.Groups {
		groups[n] = s
	}
	claims := map[string]any{
		"iss": i.URL(), "sub": g.user.Sub, "aud": ClientID, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"nonce": g.nonce, "email": g.user.Email, "email_verified": true, "name": g.user.Name, "groups": groups,
	}
	if i.Mutate != nil {
		i.Mutate(claims)
	}
	alg := i.Alg
	if alg == "" {
		alg = "RS256"
	}
	hdr, _ := json.Marshal(map[string]string{"alg": alg, "typ": "JWT", "kid": "k1"})
	body, _ := json.Marshal(claims)
	signing := b64(hdr) + "." + b64(body)
	if alg == "none" {
		return signing + "."
	}
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(fmt.Sprintf("signing: %v", err))
	}
	return signing + "." + b64(sig)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func randomString() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return b64(b)
}
