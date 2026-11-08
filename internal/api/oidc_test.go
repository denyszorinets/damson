package api_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/auth/oidc"
	"github.com/denyszorinets/damson/internal/auth/oidctest"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

const (
	callbackURL = "http://damson.test/auth/callback"
	hostName    = "damson.test"
)

// oidcFixture is controllers sharing one store and one fake provider.
type oidcFixture struct {
	t      *testing.T
	st     store.Store
	iss    *oidctest.Issuer
	now    atomic.Int64 // unix nanoseconds
	logs   *syncBuffer
	hs     []http.Handler // one per controller
	admin  string
	client oidc.Client
}

func newOIDCFixture(t *testing.T, st store.Store, controllers int) *oidcFixture {
	t.Helper()
	f := &oidcFixture{t: t, st: st, iss: oidctest.New(), logs: &syncBuffer{}}
	t.Cleanup(f.iss.Close)
	f.now.Store(time.Now().UnixNano())
	f.iss.Now = f.clock
	var err error
	f.client, err = oidc.New(oidc.Config{
		Issuer: f.iss.URL(), ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret, RedirectURL: callbackURL,
		Scopes: []string{"profile", "email", "groups"}, PostLogoutRedirectURL: "http://damson.test/", Now: f.clock,
	})
	require.NoError(t, err)
	log := slog.New(slog.NewJSONHandler(f.logs, nil))
	o, err := api.NewOIDC(api.OIDCSettings{Client: f.client, GroupsClaim: "groups", AdminRule: `'damson-admins' in claims.groups`}, log)
	require.NoError(t, err)
	admin, hash, err := tokens.GenerateWithPrefix(strings.NewReader(strings.Repeat("x", 64)), tokens.AdminPrefix)
	require.NoError(t, err)
	_, err = st.CreateSetting(t.Context(), tokens.AdminSettingKey, hash)
	require.NoError(t, err)
	f.admin = admin
	reg, err := pluginreg.New([]pluginsdk.Manifest{apiManifest("ssh", "1.0.0")}, nil)
	require.NoError(t, err)
	for i := 0; i < controllers; i++ {
		f.hs = append(f.hs, api.NewServer(api.Config{
			Addr: "127.0.0.1:0", Store: st, Data: st, UI: ui(), OIDC: o, Now: f.clock, Logger: log, Plugins: reg,
		}).Handler())
	}
	return f
}

func (f *oidcFixture) clock() time.Time { return time.Unix(0, f.now.Load()).UTC() }

func (f *oidcFixture) advance(d time.Duration) { f.now.Add(int64(d)) }

type reqOpt func(*http.Request)

func withCookies(c ...string) reqOpt {
	return func(r *http.Request) { r.Header.Set("Cookie", strings.Join(c, "; ")) }
}
func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }

func (f *oidcFixture) do(c int, method, target, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, "http://"+hostName+target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	f.hs[c].ServeHTTP(rec, req)
	return rec
}

// setCookies returns name=value of each cookie a response sets, by name.
func setCookies(rec *httptest.ResponseRecorder) map[string]*http.Cookie {
	out := map[string]*http.Cookie{}
	for _, c := range rec.Result().Cookies() {
		out[c.Name] = c
	}
	return out
}

// start begins a login on controller c and returns the provider URL and the
// login cookie.
func (f *oidcFixture) start(c int, returnTo string) (authURL string, loginCookie *http.Cookie) {
	f.t.Helper()
	rec := f.do(c, http.MethodGet, "/auth/login?return_to="+url.QueryEscape(returnTo), "")
	require.Equal(f.t, http.StatusFound, rec.Code, rec.Body.String())
	loginCookie = setCookies(rec)["__Host-damson_login"]
	require.NotNil(f.t, loginCookie)
	return rec.Header().Get("Location"), loginCookie
}

// approve visits the provider as user and returns the callback path with code and state.
func (f *oidcFixture) approve(authURL, user string) string {
	f.t.Helper()
	u, err := url.Parse(authURL)
	require.NoError(f.t, err)
	q := u.Query()
	q.Set("login_hint", user)
	u.RawQuery = q.Encode()
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Get(u.String())
	require.NoError(f.t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(f.t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(f.t, err)
	return "/auth/callback?" + loc.RawQuery
}

func cookieHeader(c *http.Cookie) string { return c.Name + "=" + c.Value }

// login runs the whole flow on controller c and returns the session cookie.
func (f *oidcFixture) login(c int, user string) *http.Cookie {
	f.t.Helper()
	authURL, lc := f.start(c, "/")
	rec := f.do(c, http.MethodGet, f.approve(authURL, user), "", withCookies(cookieHeader(lc)))
	require.Equal(f.t, http.StatusFound, rec.Code, rec.Body.String())
	sc := setCookies(rec)["__Host-damson_session"]
	require.NotNil(f.t, sc)
	return sc
}

func (f *oidcFixture) me(c int, sc *http.Cookie) api.Me {
	f.t.Helper()
	rec := f.do(c, http.MethodGet, "/api/v1/me", "", withCookies(cookieHeader(sc)))
	require.Equal(f.t, http.StatusOK, rec.Code, rec.Body.String())
	var m api.Me
	require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &m))
	return m
}

func TestOIDCLoginFlow(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newOIDCFixture(t, st, 1)
		authURL, lc := f.start(0, "/jobs?x=1")
		assert.True(t, lc.HttpOnly && lc.Secure && lc.Path == "/" && lc.SameSite == http.SameSiteLaxMode, "login cookie %+v", lc)
		assert.Equal(t, "no-store", f.do(0, "GET", "/auth/login", "").Header().Get("Cache-Control"))

		rec := f.do(0, http.MethodGet, f.approve(authURL, oidctest.Alice.Sub), "", withCookies(cookieHeader(lc)))
		require.Equal(t, http.StatusFound, rec.Code)
		assert.Equal(t, "/jobs?x=1", rec.Header().Get("Location"))
		cs := setCookies(rec)
		sc := cs["__Host-damson_session"]
		require.NotNil(t, sc)
		assert.True(t, sc.HttpOnly && sc.Secure && sc.Path == "/" && sc.SameSite == http.SameSiteLaxMode && sc.Domain == "")
		assert.Equal(t, int((12 * time.Hour).Seconds()), sc.MaxAge)
		assert.Equal(t, -1, cs["__Host-damson_login"].MaxAge, "the login cookie is cleared")

		me := f.me(0, sc)
		assert.Equal(t, "user", me.Kind)
		assert.Equal(t, "user:alice-sub", me.Actor)
		assert.Equal(t, "alice@example.org", me.Email)
		assert.True(t, me.InstanceAdmin)
		assert.Equal(t, []string{"damson-admins"}, me.Groups)
		assert.NotEmpty(t, me.CSRFToken)

		bob := f.me(0, f.login(0, oidctest.Bob.Sub))
		assert.False(t, bob.InstanceAdmin)
		assert.Equal(t, []string{"team-ops"}, bob.Groups)

		// The cookie value is not what is stored: only its hash is.
		_, err := st.FindSession(t.Context(), sc.Value, f.clock(), time.Hour)
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = st.FindSession(t.Context(), tokens.Hash(sc.Value), f.clock(), time.Hour)
		assert.NoError(t, err)
		// Nothing secret reaches the logs.
		for _, secret := range []string{sc.Value, lc.Value, "code=", oidctest.ClientSecret} {
			assert.NotContains(t, f.logs.String(), secret)
		}
	})
}

func TestOIDCReturnToCannotLeaveTheSite(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newOIDCFixture(t, st, 1)
		for _, bad := range []string{"//evil.example", "https://evil.example/x", "/\\evil.example", "javascript:alert(1)", "evil", "/a\r\nSet-Cookie: x=y"} {
			authURL, lc := f.start(0, bad)
			rec := f.do(0, http.MethodGet, f.approve(authURL, oidctest.Alice.Sub), "", withCookies(cookieHeader(lc)))
			assert.Equal(t, "/", rec.Header().Get("Location"), "return_to %q", bad)
		}
	})
}

func TestOIDCCallbackRejections(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newOIDCFixture(t, st, 1)
		cb := func(path string, lc *http.Cookie) *httptest.ResponseRecorder {
			if lc == nil {
				return f.do(0, http.MethodGet, path, "")
			}
			return f.do(0, http.MethodGet, path, "", withCookies(cookieHeader(lc)))
		}
		noSession := func(rec *httptest.ResponseRecorder, why string) {
			t.Helper()
			assert.Equal(t, http.StatusUnauthorized, rec.Code, why)
			assert.Nil(t, setCookies(rec)["__Host-damson_session"], why)
		}

		// The state works once.
		authURL, lc := f.start(0, "/")
		path := f.approve(authURL, oidctest.Alice.Sub)
		assert.Equal(t, http.StatusFound, cb(path, lc).Code)
		noSession(cb(path, lc), "replayed state")

		// No binding cookie, or the cookie of another login.
		authURL, _ = f.start(0, "/")
		noSession(cb(f.approve(authURL, oidctest.Alice.Sub), nil), "no login cookie")
		authURL, _ = f.start(0, "/")
		_, other := f.start(0, "/")
		noSession(cb(f.approve(authURL, oidctest.Alice.Sub), other), "another browser's cookie")

		// An unknown or missing state; an expired one.
		noSession(cb("/auth/callback?code=x&state=nope", lc), "unknown state")
		noSession(cb("/auth/callback?code=x", lc), "no state")
		authURL, lc = f.start(0, "/")
		path = f.approve(authURL, oidctest.Alice.Sub)
		f.advance(11 * time.Minute)
		noSession(cb(path, lc), "expired state")

		// The provider refused, or sent no code.
		authURL, lc = f.start(0, "/")
		state := mustQuery(t, authURL, "state")
		noSession(cb("/auth/callback?error=access_denied&state="+state, lc), "provider error")
		authURL, lc = f.start(0, "/")
		noSession(cb("/auth/callback?state="+mustQuery(t, authURL, "state"), lc), "no code")
	})
}

func mustQuery(t *testing.T, raw, key string) string {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u.Query().Get(key)
}

func TestOIDCBadIDTokens(t *testing.T) {
	cases := map[string]func(i *oidctest.Issuer){
		"wrong audience": func(i *oidctest.Issuer) { i.Mutate = func(c map[string]any) { c["aud"] = "other" } },
		"wrong issuer":   func(i *oidctest.Issuer) { i.Mutate = func(c map[string]any) { c["iss"] = "http://evil.example" } },
		"wrong nonce":    func(i *oidctest.Issuer) { i.Mutate = func(c map[string]any) { c["nonce"] = "forged" } },
		"expired": func(i *oidctest.Issuer) {
			i.Mutate = func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }
		},
		"alg none":    func(i *oidctest.Issuer) { i.Alg = "none" },
		"no id token": func(i *oidctest.Issuer) { i.NoIDToken = true },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			forEachBackend(t, func(t *testing.T, st store.Store) {
				f := newOIDCFixture(t, st, 1)
				setup(f.iss)
				authURL, lc := f.start(0, "/")
				rec := f.do(0, http.MethodGet, f.approve(authURL, oidctest.Alice.Sub), "", withCookies(cookieHeader(lc)))
				assert.Equal(t, http.StatusUnauthorized, rec.Code)
				assert.Nil(t, setCookies(rec)["__Host-damson_session"])
				assert.NotContains(t, rec.Body.String(), "forged")
			})
		})
	}
}

func TestOIDCProviderDown(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newOIDCFixture(t, st, 1)
		f.iss.Down.Store(true)
		assert.Equal(t, http.StatusBadGateway, f.do(0, http.MethodGet, "/auth/login", "").Code)
		f.iss.Down.Store(false)
		assert.Equal(t, http.StatusFound, f.do(0, http.MethodGet, "/auth/login", "").Code, "recovers when the provider is back")
	})
}

// A login started on one controller finishes on another, and a logout on one
// is seen by the other at once.
func TestOIDCAcrossControllers(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newOIDCFixture(t, st, 2)
		authURL, lc := f.start(0, "/")
		rec := f.do(1, http.MethodGet, f.approve(authURL, oidctest.Alice.Sub), "", withCookies(cookieHeader(lc)))
		require.Equal(t, http.StatusFound, rec.Code)
		sc := setCookies(rec)["__Host-damson_session"]
		m := f.me(0, sc)
		require.Equal(t, "user:alice-sub", f.me(1, sc).Actor)

		out := f.do(1, http.MethodPost, "/auth/logout", "", withCookies(cookieHeader(sc)), withHeader(api.CSRFHeader, m.CSRFToken))
		require.Equal(t, http.StatusOK, out.Code, out.Body.String())
		var body struct {
			EndSessionURL string `json:"end_session_url"`
		}
		require.NoError(t, json.Unmarshal(out.Body.Bytes(), &body))
		assert.Contains(t, body.EndSessionURL, f.iss.URL()+"/logout")
		assert.Equal(t, -1, setCookies(out)["__Host-damson_session"].MaxAge)

		for c := 0; c < 2; c++ {
			assert.Equal(t, http.StatusUnauthorized, f.do(c, http.MethodGet, "/api/v1/me", "", withCookies(cookieHeader(sc))).Code, "controller %d", c)
		}
	})
}

func TestOIDCSessionLifetime(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newOIDCFixture(t, st, 1)
		sc := f.login(0, oidctest.Alice.Sub)
		get := func() int { return f.do(0, http.MethodGet, "/api/v1/me", "", withCookies(cookieHeader(sc))).Code }

		// Activity keeps a session alive past the 2h idle limit...
		for i := 0; i < 5; i++ {
			f.advance(90 * time.Minute)
			require.Equal(t, http.StatusOK, get(), "step %d", i)
		}
		// ...but 2h without a request ends it.
		f.advance(2*time.Hour + time.Minute)
		assert.Equal(t, http.StatusUnauthorized, get(), "idle")

		// Even an active session ends at 12h.
		sc = f.login(0, oidctest.Alice.Sub)
		for i := 0; i < 7; i++ { // 11h40m
			f.advance(100 * time.Minute)
			require.Equal(t, http.StatusOK, get(), "step %d", i)
		}
		f.advance(100 * time.Minute) // 13h20m
		assert.Equal(t, http.StatusUnauthorized, get(), "absolute limit")
	})
}

func TestOIDCCSRF(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newOIDCFixture(t, st, 1)
		sc := f.login(0, oidctest.Alice.Sub)
		csrf := f.me(0, sc).CSRFToken
		body := `{"path":"team-a"}`
		post := func(opts ...reqOpt) *httptest.ResponseRecorder {
			return f.do(0, http.MethodPost, "/api/v1/projects", body, append([]reqOpt{withCookies(cookieHeader(sc))}, opts...)...)
		}
		assert.Equal(t, http.StatusForbidden, post().Code, "no CSRF header")
		assert.Equal(t, http.StatusForbidden, post(withHeader(api.CSRFHeader, "wrong")).Code, "wrong CSRF header")
		other := f.login(0, oidctest.Alice.Sub)
		assert.Equal(t, http.StatusForbidden, post(withHeader(api.CSRFHeader, f.me(0, other).CSRFToken)).Code, "another session's token")
		assert.Equal(t, http.StatusForbidden, post(withHeader(api.CSRFHeader, csrf), withHeader("Origin", "http://evil.example")).Code, "foreign origin")
		assert.Equal(t, http.StatusForbidden, post(withHeader(api.CSRFHeader, csrf), withHeader("Sec-Fetch-Site", "cross-site")).Code, "cross-site fetch")
		rec := post(withHeader(api.CSRFHeader, csrf), withHeader("Origin", "http://"+hostName), withHeader("Sec-Fetch-Site", "same-origin"))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

		evs, err := st.ListAuditEvents(t.Context(), store.AuditFilter{TargetType: store.AuditTargetProject}, 10)
		require.NoError(t, err)
		require.Len(t, evs, 1)
		assert.Equal(t, "user:alice-sub", evs[0].Actor)

		// Reads need no token; logout needs it.
		assert.Equal(t, http.StatusOK, f.do(0, http.MethodGet, "/api/v1/projects", "", withCookies(cookieHeader(sc))).Code)
		assert.Equal(t, http.StatusForbidden, f.do(0, http.MethodPost, "/auth/logout", "", withCookies(cookieHeader(sc))).Code)
		assert.Equal(t, http.StatusOK, f.me2(sc).Code, "a refused logout leaves the session")
	})
}

func (f *oidcFixture) me2(sc *http.Cookie) *httptest.ResponseRecorder {
	return f.do(0, http.MethodGet, "/api/v1/me", "", withCookies(cookieHeader(sc)))
}

func TestOIDCEndsAnonymousAccess(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newOIDCFixture(t, st, 1)
		alice, bob := f.login(0, oidctest.Alice.Sub), f.login(0, oidctest.Bob.Sub)

		// No credentials: governed, admin and formerly open routes all need a login.
		for _, p := range []string{"/api/v1/jobs", "/api/v1/runs", "/api/v1/agents", "/api/v1/pools", "/api/v1/plugins", "/api/v1/me", "/api/v1/projects", "/api/v1/audit"} {
			assert.Equal(t, http.StatusUnauthorized, f.do(0, http.MethodGet, p, "").Code, p)
		}
		// Open on purpose.
		assert.Equal(t, http.StatusOK, f.do(0, http.MethodGet, "/healthz", "").Code)
		assert.Equal(t, http.StatusOK, f.do(0, http.MethodGet, "/api/v1/secret-types", "").Code)

		// A user with no rule granting a role sees nothing.
		assert.Equal(t, http.StatusNotFound, f.do(0, http.MethodGet, "/api/v1/jobs", "", withCookies(cookieHeader(bob))).Code, "no role in the project")
		assert.Equal(t, http.StatusOK, f.do(0, http.MethodGet, "/api/v1/projects", "", withCookies(cookieHeader(bob))).Code, "the list shows only what the caller can see")
		assert.Equal(t, http.StatusForbidden, f.do(0, http.MethodGet, "/api/v1/audit", "", withCookies(cookieHeader(bob))).Code)
		assert.Equal(t, http.StatusOK, f.do(0, http.MethodGet, "/api/v1/agents", "", withCookies(cookieHeader(bob))).Code, "any login may read the inventory metadata")
		// An instance admin may do everything.
		for _, p := range []string{"/api/v1/jobs", "/api/v1/projects", "/api/v1/audit"} {
			assert.Equal(t, http.StatusOK, f.do(0, http.MethodGet, p, "", withCookies(cookieHeader(alice))).Code, p)
		}
		// The admin token still works, and require_token no longer matters.
		assert.Equal(t, http.StatusOK, f.do(0, http.MethodGet, "/api/v1/jobs", "", withHeader("Authorization", "Bearer "+f.admin)).Code)
		assert.Equal(t, http.StatusOK, f.do(0, http.MethodGet, "/api/v1/projects", "", withHeader("Authorization", "Bearer "+f.admin)).Code)
	})
}

// A token that is sent decides, whatever the cookie says.
func TestOIDCBearerBeatsCookie(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newOIDCFixture(t, st, 1)
		alice := f.login(0, oidctest.Alice.Sub)
		for _, p := range []string{"/api/v1/jobs", "/api/v1/projects", "/api/v1/me"} {
			rec := f.do(0, http.MethodGet, p, "", withCookies(cookieHeader(alice)), withHeader("Authorization", "Bearer "+tokens.AdminPrefix+"nope"))
			assert.Equal(t, http.StatusUnauthorized, rec.Code, p)
		}
		rec := f.do(0, http.MethodGet, "/api/v1/me", "", withCookies(cookieHeader(alice)), withHeader("Authorization", "Bearer "+f.admin))
		var m api.Me
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
		assert.Equal(t, "admin_token", m.Kind, "the bearer token, not the session, is the principal")
	})
}

func TestMeWithoutLogin(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		var m api.Me
		f.callJSON(t, "GET", "/api/v1/me", "", 200, &m)
		assert.Equal(t, "admin_token", m.Kind)
		rec := f.do("GET", "/api/v1/me", "", "")
		require.Equal(t, 200, rec.Code)
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
		assert.Equal(t, "anonymous", m.Kind)
		// And /auth/* does not exist without login configured.
		assert.Equal(t, 404, f.do("GET", "/auth/login", "", "").Code)
	})
}

func TestOIDCLoginIsRateLimited(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newOIDCFixture(t, st, 1)
		var limited int
		for i := 0; i < 60; i++ {
			rec := f.do(0, http.MethodGet, "/auth/login", "")
			if rec.Code == http.StatusTooManyRequests {
				limited++
				assert.NotEmpty(t, rec.Header().Get("Retry-After"))
			}
		}
		assert.Positive(t, limited, "an unauthenticated endpoint that writes a row needs a limit")
		f.advance(5 * time.Second)
		assert.Equal(t, http.StatusFound, f.do(0, http.MethodGet, "/auth/login", "").Code, "the bucket refills")
	})
}

func TestOIDCNewLoginEndsThePreviousSession(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newOIDCFixture(t, st, 1)
		old := f.login(0, oidctest.Alice.Sub)
		authURL, lc := f.start(0, "/")
		rec := f.do(0, http.MethodGet, f.approve(authURL, oidctest.Alice.Sub), "", withCookies(cookieHeader(lc), cookieHeader(old)))
		require.Equal(t, http.StatusFound, rec.Code)
		fresh := setCookies(rec)["__Host-damson_session"]
		assert.NotEqual(t, old.Value, fresh.Value, "a fresh token on every login")
		assert.Equal(t, http.StatusUnauthorized, f.me2(old).Code, "the replaced session is gone")
		assert.Equal(t, http.StatusOK, f.me2(fresh).Code)
	})
}
