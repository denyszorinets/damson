package api_test

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/secrets"
	secretsdb "github.com/denyszorinets/damson/internal/secrets/db"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

const secretValue = "s3cr3t-api-value-XYZ"

type secretsFixture struct {
	h     http.Handler
	logs  *syncBuffer
	st    store.Store
	admin string
}

func newSecretsFixture(t *testing.T, st store.Store, withBackend bool) *secretsFixture {
	t.Helper()
	logs := &syncBuffer{}
	cfg := api.Config{
		Addr: "127.0.0.1:0", Store: st, Data: st, UI: ui(),
		Now:    func() time.Time { return t0 },
		Logger: slog.New(slog.NewJSONHandler(logs, nil)),
	}
	if withBackend {
		k := make([]byte, 32)
		_, _ = rand.Read(k)
		ring, err := secrets.NewKeyring(base64.StdEncoding.EncodeToString(k), "")
		require.NoError(t, err)
		var n atomic.Int64
		cfg.Secrets = secretsdb.New(st, ring, rand.Reader, func() time.Time { return t0 }, func() string { return fmt.Sprintf("s%d-%s", n.Add(1), uuid.NewString()) })
	}
	admin, hash, err := tokens.GenerateWithPrefix(rand.Reader, tokens.AdminPrefix)
	require.NoError(t, err)
	_, err = st.CreateSetting(t.Context(), tokens.AdminSettingKey, hash)
	require.NoError(t, err)
	return &secretsFixture{h: api.NewServer(cfg).Handler(), logs: logs, st: st, admin: admin}
}

func (f *secretsFixture) do(method, target, body, token string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

// noValue fails if the value, or its base64, is in a response or in the logs.
func (f *secretsFixture) noValue(t *testing.T, recs ...*httptest.ResponseRecorder) {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString([]byte(secretValue))
	all := f.logs.String()
	for _, r := range recs {
		all += r.Body.String()
	}
	assert.NotContains(t, all, secretValue)
	assert.NotContains(t, all, b64)
}

func TestSecretsRequireTheAdminToken(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, true)
		pool, _, _ := tokens.New()
		body := `{"type":"generic","fields":{"k":"` + secretValue + `"}}`
		for _, tc := range []struct{ name, token string }{
			{"missing", ""},
			{"pool token", pool},
			{"wrong admin token", tokens.AdminPrefix + "nope"},
		} {
			for _, r := range [][3]string{{"GET", "/api/v1/secrets", ""}, {"PUT", "/api/v1/secrets/p/x", body}, {"DELETE", "/api/v1/secrets/p/x", ""}} {
				rec := f.do(r[0], r[1], r[2], tc.token)
				assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s %s", tc.name, r[0], r[1])
				assert.NotEmpty(t, rec.Header().Get("WWW-Authenticate"))
				f.noValue(t, rec)
			}
		}
		// The auth check comes before body parsing: junk is a 401, not a 422.
		assert.Equal(t, http.StatusUnauthorized, f.do("PUT", "/api/v1/secrets/p/x", `{`, "").Code)
		// A revoked token (empty hash) denies, and so does no token at all.
		_, err := st.UpdateSetting(t.Context(), tokens.AdminSettingKey, "", 1)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, f.do("GET", "/api/v1/secrets", "", f.admin).Code)
		// The types table is public.
		assert.Equal(t, http.StatusOK, f.do("GET", "/api/v1/secret-types", "", "").Code)
	})
}

func TestSecretsLifecycle(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, true)
		put := func(target, body string) *httptest.ResponseRecorder {
			rec := f.do("PUT", target, body, f.admin)
			f.noValue(t, rec)
			return rec
		}
		fields := `"fields":{"password":"` + secretValue + `","username":"root"}`
		rec := put("/api/v1/secrets/proj/db", `{"type":"password",`+fields+`}`)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		var m api.SecretMeta
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
		assert.Equal(t, "secret://proj/db", m.Ref)
		assert.Equal(t, []string{"password", "username"}, m.Fields)
		assert.EqualValues(t, 1, m.CurrentVersion)

		// A new version is 200; conditional writes.
		rec = put("/api/v1/secrets/proj/db", `{"type":"password","if_version":1,`+fields+`}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, http.StatusConflict, put("/api/v1/secrets/proj/db", `{"type":"password","if_version":1,`+fields+`}`).Code)
		assert.Equal(t, http.StatusConflict, put("/api/v1/secrets/proj/db", `{"type":"password","if_version":0,`+fields+`}`).Code)
		assert.Equal(t, http.StatusConflict, put("/api/v1/secrets/proj/db", `{"type":"token","fields":{"token":"`+secretValue+`"}}`).Code, "type change")
		assert.Equal(t, http.StatusUnprocessableEntity, put("/api/v1/secrets/proj/db", `{"type":"password","fields":{"username":"`+secretValue+`"}}`).Code, "missing required field")
		assert.Equal(t, http.StatusUnprocessableEntity, put("/api/v1/secrets/_proj/x", `{"type":"generic","fields":{"k":"`+secretValue+`"}}`).Code, "bad scope")
		assert.Equal(t, http.StatusUnprocessableEntity, put("/api/v1/secrets/p/x", `{"type":"nope","fields":{"k":"`+secretValue+`"}}`).Code, "bad type")
		big := `{"type":"generic","fields":{"k":"` + strings.Repeat("x", secrets.MaxFieldBytes+1) + `"}}`
		assert.Equal(t, http.StatusRequestEntityTooLarge, put("/api/v1/secrets/p/x", big).Code)

		// A multi-segment scope travels as one percent-encoded path segment.
		rec = put("/api/v1/secrets/infra%2Fbilling/key", `{"type":"generic","fields":{"k":"`+secretValue+`"}}`)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
		assert.Equal(t, "secret://infra/billing/key", m.Ref)
		assert.Equal(t, "infra/billing", m.Scope)

		// The list holds metadata only, paged.
		var page struct {
			Items      []api.SecretMeta `json:"items"`
			NextCursor string           `json:"next_cursor"`
		}
		rec = f.do("GET", "/api/v1/secrets?limit=1", "", f.admin)
		require.Equal(t, http.StatusOK, rec.Code)
		f.noValue(t, rec)
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
		require.Len(t, page.Items, 1)
		require.NotEmpty(t, page.NextCursor)
		rec = f.do("GET", "/api/v1/secrets?limit=5&cursor="+page.NextCursor, "", f.admin)
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
		assert.Len(t, page.Items, 1)
		rec = f.do("GET", "/api/v1/secrets?scope=infra%2Fbilling", "", f.admin)
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
		require.Len(t, page.Items, 1)
		assert.Equal(t, "infra/billing", page.Items[0].Scope)

		assert.Equal(t, http.StatusNoContent, f.do("DELETE", "/api/v1/secrets/proj/db", "", f.admin).Code)
		assert.Equal(t, http.StatusNotFound, f.do("DELETE", "/api/v1/secrets/proj/db", "", f.admin).Code)
		f.noValue(t)
	})
}

func TestSecretsDisabledWithoutBackend(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, false)
		for _, r := range [][3]string{{"GET", "/api/v1/secrets", ""}, {"PUT", "/api/v1/secrets/p/x", `{"type":"generic","fields":{"k":"v"}}`}, {"DELETE", "/api/v1/secrets/p/x", ""}} {
			assert.Equal(t, http.StatusServiceUnavailable, f.do(r[0], r[1], r[2], f.admin).Code, r[1])
		}
		// Still guarded: no token, no information.
		assert.Equal(t, http.StatusUnauthorized, f.do("GET", "/api/v1/secrets", "", "").Code)
	})
}

// Huma validates the body before the handler runs and its own errors carry the
// submitted value internally. None of that may reach a response or a log.
func TestSecretsRejectedBodiesDoNotEchoValues(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, true)
		v := secretValue
		for _, tc := range []struct {
			name, body, ctype string
			want              int
		}{
			{"truncated json", `{"type":"generic","fields":{"k":"` + v + `"}`, "application/json", 400},
			{"non-string value", `{"type":"generic","fields":{"k":"` + v + `","n":1}}`, "application/json", 422},
			{"unknown property", `{"type":"generic","fields":{"k":"` + v + `"},"extra":1}`, "application/json", 422},
			{"negative if_version", `{"type":"generic","if_version":-1,"fields":{"k":"` + v + `"}}`, "application/json", 422},
			{"bad field name", `{"type":"generic","fields":{"Bad Name":"` + v + `"}}`, "application/json", 422},
			{"too large", `{"type":"generic","fields":{"k":"` + v + strings.Repeat("x", 2<<20) + `"}}`, "application/json", 413},
			{"wrong content type", `{"type":"generic","fields":{"k":"` + v + `"}}`, "text/plain", 415},
		} {
			req := httptest.NewRequest("PUT", "/api/v1/secrets/p/x", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.ctype)
			req.Header.Set("Authorization", "Bearer "+f.admin)
			rec := httptest.NewRecorder()
			f.h.ServeHTTP(rec, req)
			assert.Equal(t, tc.want, rec.Code, "%s: %s", tc.name, rec.Body.String())
			f.noValue(t, rec)
		}
	})
}

// Every secrets operation except the public types table is guarded: it names
// the bearerAuth scheme and answers 401 without a token, so a route added
// later without the guard fails here.
func TestSecretsOperationsAreGuarded(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, true)
		srv := api.NewServer(api.Config{Addr: "127.0.0.1:0", Store: st, Data: st, UI: ui(), Logger: slog.New(slog.DiscardHandler)})
		doc := srv.Handler()
		rec := httptest.NewRecorder()
		doc.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/openapi.json", nil))
		var spec struct {
			Paths map[string]map[string]struct {
				OperationID string           `json:"operationId"`
				Tags        []string         `json:"tags"`
				Security    []map[string]any `json:"security"`
			} `json:"paths"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &spec))
		checked := 0
		for path, ops := range spec.Paths {
			for method, op := range ops {
				if len(op.Tags) == 0 || op.Tags[0] != "secrets" || op.OperationID == "list-secret-types" {
					continue
				}
				checked++
				assert.NotEmpty(t, op.Security, op.OperationID)
				target := strings.NewReplacer("{scope}", "p", "{name}", "x").Replace("/api/v1" + path)
				assert.Equal(t, http.StatusUnauthorized, f.do(strings.ToUpper(method), target, "", "").Code, op.OperationID)
			}
		}
		assert.GreaterOrEqual(t, checked, 3)
	})
}

func TestAdminTokenSchemeAndMessages(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, true)
		// The scheme name is case-insensitive.
		req := httptest.NewRequest("GET", "/api/v1/secrets", nil)
		req.Header.Set("Authorization", "bearer "+f.admin)
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		// A revoked token and a wrong one are indistinguishable.
		wrong := f.do("GET", "/api/v1/secrets", "", tokens.AdminPrefix+"nope")
		_, err := st.UpdateSetting(t.Context(), tokens.AdminSettingKey, "", 1)
		require.NoError(t, err)
		revoked := f.do("GET", "/api/v1/secrets", "", f.admin)
		type p struct{ Detail string }
		var a, b p
		require.NoError(t, json.Unmarshal(wrong.Body.Bytes(), &a))
		require.NoError(t, json.Unmarshal(revoked.Body.Bytes(), &b))
		assert.Equal(t, a.Detail, b.Detail)
	})
}
