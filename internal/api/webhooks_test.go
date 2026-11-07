package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

func (f *schedFixture) newHook(t *testing.T, body string) api.CreatedWebhook {
	t.Helper()
	var w api.CreatedWebhook
	f.callJSON(t, http.MethodPost, "/api/v1/webhooks", body, 201, &w)
	return w
}

func (f *schedFixture) doJSON(method, target, body string) *httptest.ResponseRecorder {
	return f.doJSONHeaders(method, target, body, nil)
}

func (f *schedFixture) doJSONHeaders(method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func TestWebhooksRequireTheAdminToken(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		pool, _, _ := tokens.New()
		body := fmt.Sprintf(`{"job_id":%q,"name":"n"}`, f.jobID)
		for _, tok := range []string{"", pool, tokens.AdminPrefix + "nope"} {
			for _, r := range [][3]string{
				{"GET", "/api/v1/webhooks", ""}, {"GET", "/api/v1/webhooks/x", ""}, {"POST", "/api/v1/webhooks", body}, {"DELETE", "/api/v1/webhooks/x", ""},
			} {
				assert.Equal(t, http.StatusUnauthorized, f.do(r[0], r[1], r[2], tok).Code, "%s %s", r[0], r[1])
			}
		}
	})
}

func TestWebhookLifecycle(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		w := f.newHook(t, fmt.Sprintf(`{"job_id":%q,"name":"ci","options":{"env":"prod"}}`, f.jobID))
		require.Len(t, w.Token, 43)
		assert.Equal(t, "/hooks/"+w.Token, w.Path)
		assert.Equal(t, map[string]any{"env": "prod"}, w.Options)

		// The token is shown once: no read returns it or its hash.
		get := f.do("GET", "/api/v1/webhooks/"+w.ID, "", f.admin)
		list := f.do("GET", "/api/v1/webhooks?job_id="+f.jobID, "", f.admin)
		for _, rec := range []string{get.Body.String(), list.Body.String()} {
			assert.NotContains(t, rec, w.Token)
			assert.NotContains(t, rec, tokens.Hash(w.Token))
		}

		rec := f.do("POST", w.Path, "", "")
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		var started struct {
			RunID string `json:"run_id"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))
		run, err := st.GetRun(t.Context(), started.RunID)
		require.NoError(t, err)
		var trig map[string]string
		require.NoError(t, json.Unmarshal(run.Trigger, &trig))
		assert.Equal(t, map[string]string{"type": "webhook", "webhook_id": w.ID}, trig)
		assert.JSONEq(t, `{"env":"prod"}`, string(run.Options))

		var got api.Webhook
		f.callJSON(t, "GET", "/api/v1/webhooks/"+w.ID, "", 200, &got)
		assert.NotNil(t, got.LastUsedAt)

		// Revoke: the token stops working; the answer is the one an unknown token gets.
		f.callJSON(t, "DELETE", "/api/v1/webhooks/"+w.ID, "", 204, nil)
		f.callJSON(t, "DELETE", "/api/v1/webhooks/"+w.ID, "", 204, nil)
		revoked := f.do("POST", w.Path, "", "")
		unknown := f.do("POST", "/hooks/"+strings.Repeat("A", 43), "", "")
		assert.Equal(t, http.StatusNotFound, revoked.Code)
		assert.Equal(t, http.StatusNotFound, unknown.Code)
		assert.Equal(t, stripID(unknown.Body.String()), stripID(revoked.Body.String()))
		f.callJSON(t, "GET", "/api/v1/webhooks/"+w.ID, "", 200, &got)
		assert.True(t, got.Revoked)
		f.callJSON(t, "DELETE", "/api/v1/webhooks/nope", "", 404, nil)

		// The name can be reused after a revoke; a live duplicate conflicts.
		f.newHook(t, fmt.Sprintf(`{"job_id":%q,"name":"ci"}`, f.jobID))
		f.callJSON(t, "POST", "/api/v1/webhooks", fmt.Sprintf(`{"job_id":%q,"name":"ci"}`, f.jobID), 409, nil)

		var audit struct {
			Items []api.AuditEvent `json:"items"`
		}
		f.callJSON(t, "GET", "/api/v1/audit?target_type=webhook", "", 200, &audit)
		for _, e := range audit.Items {
			assert.NotContains(t, string(e.Details), w.Token)
			assert.NotContains(t, string(e.Details), "prod")
		}
		assert.Len(t, audit.Items, 3, "create, revoke (once), create")

		// Neither the logs nor the request line carry the token.
		assert.NotContains(t, f.logs.String(), w.Token)
		assert.Contains(t, f.logs.String(), "/hooks/[redacted]")
	})
}

func stripID(s string) string {
	var m map[string]any
	_ = json.Unmarshal([]byte(s), &m)
	delete(m, "request_id")
	b, _ := json.Marshal(m)
	return string(b)
}

func TestWebhookRejectsMalformedRequests(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		w := f.newHook(t, fmt.Sprintf(`{"job_id":%q,"name":"ci"}`, f.jobID))
		assert.Equal(t, http.StatusNotFound, f.do("POST", "/hooks/short", "", "").Code)
		assert.Equal(t, http.StatusNotFound, f.do("POST", "/hooks/"+strings.Repeat("!", 43), "", "").Code)
		assert.Equal(t, http.StatusMethodNotAllowed, f.do("GET", w.Path, "", "").Code)
	})
}

func TestWebhookOptionsMapping(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		mapping := `{"env": body.environment}`
		w := f.newHook(t, fmt.Sprintf(`{"job_id":%q,"name":"map","options_mapping":%q}`, f.jobID, mapping))
		post := func(body string) (int, string) {
			rec := f.doJSON("POST", w.Path, body)
			return rec.Code, rec.Body.String()
		}
		code, out := post(`{"environment":"prod"}`)
		require.Equal(t, http.StatusAccepted, code, out)
		var started struct {
			RunID string `json:"run_id"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &started))
		run, _ := st.GetRun(t.Context(), started.RunID)
		assert.JSONEq(t, `{"env":"prod"}`, string(run.Options))

		// An option the schema refuses: 422, and the value is not echoed.
		code, out = post(`{"environment":"SECRET-STAGING-VALUE"}`)
		assert.Equal(t, http.StatusUnprocessableEntity, code)
		assert.NotContains(t, out, "SECRET-STAGING-VALUE")
		// The mapping fails on this body: 422, with no part of the body.
		code, out = post(`{"other":"SECRET-BODY-VALUE"}`)
		assert.Equal(t, http.StatusUnprocessableEntity, code, out)
		assert.NotContains(t, out, "SECRET-BODY-VALUE")
		code, out = post(`not json`)
		assert.Equal(t, http.StatusUnprocessableEntity, code)
		assert.NotContains(t, out, "not json")
		assert.NotContains(t, f.logs.String(), "SECRET-")

		// Headers and the query reach the mapping; credentials do not.
		h := f.newHook(t, fmt.Sprintf(`{"job_id":%q,"name":"hdr","options_mapping":%q}`, f.jobID,
			`{"env": has(headers.authorization) ? "staging" : string(headers["x-env"]) + string(query.suffix)}`))
		rec := f.doJSONHeaders("POST", h.Path+"?suffix=", `{}`, map[string]string{"X-Env": "dev", "Authorization": "Bearer x"})
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		var s2 struct {
			RunID string `json:"run_id"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &s2))
		run, _ = st.GetRun(t.Context(), s2.RunID)
		assert.JSONEq(t, `{"env":"dev"}`, string(run.Options))
	})
}

func TestWebhookCreateValidation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		for name, body := range map[string]string{
			"bad mapping":     fmt.Sprintf(`{"job_id":%q,"name":"a","options_mapping":"{{"}`, f.jobID),
			"options invalid": fmt.Sprintf(`{"job_id":%q,"name":"a","options":{"env":"staging"}}`, f.jobID),
		} {
			assert.Equal(t, http.StatusUnprocessableEntity, f.do("POST", "/api/v1/webhooks", body, f.admin).Code, name)
		}
		assert.Equal(t, http.StatusNotFound, f.do("POST", "/api/v1/webhooks", `{"job_id":"nope","name":"a"}`, f.admin).Code)
	})
}

func TestWebhookIsRateLimitedPerToken(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		a := f.newHook(t, fmt.Sprintf(`{"job_id":%q,"name":"a"}`, f.jobID))
		b := f.newHook(t, fmt.Sprintf(`{"job_id":%q,"name":"b"}`, f.jobID))
		var ok, limited int
		for range 40 { // the test clock does not move: no refill
			switch rec := f.do("POST", a.Path, "", ""); rec.Code {
			case http.StatusAccepted:
				ok++
			case http.StatusTooManyRequests:
				limited++
				assert.NotEmpty(t, rec.Header().Get("Retry-After"))
			default:
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
		}
		assert.Equal(t, 20, ok)
		assert.Equal(t, 20, limited)
		assert.Equal(t, http.StatusAccepted, f.do("POST", b.Path, "", "").Code, "another token has its own bucket")
	})
}

const hookSecretOptions = `{"type":"object","properties":{"env":{"type":"string","default":"dev"},"token":{"type":"string","x-damson-secret":true}}}`

func (f *schedFixture) secretJob(t *testing.T) string {
	t.Helper()
	var job jobDetailBody
	f.callJSON(t, http.MethodPost, "/api/v1/jobs", wrapSpec(specDoc("secret-job", hookSecretOptions, "ssh", `{"host":"h"}`)), 201, &job)
	return job.Job.ID
}

// The token never reaches the log, even through an unclean path the mux redirects.
func TestWebhookTokenIsRedactedFromUncleanPaths(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		w := f.newHook(t, fmt.Sprintf(`{"job_id":%q,"name":"ci"}`, f.jobID))
		for _, p := range []string{"//hooks/" + w.Token, "/./hooks/" + w.Token, "/x/../hooks/" + w.Token} {
			f.do("POST", p, "", "")
		}
		assert.NotContains(t, f.logs.String(), w.Token)
	})
}

func TestWebhookSecretOptions(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		job := f.secretJob(t)
		// A plaintext secret is refused at create, with or without a mapping, and nothing is stored.
		for _, m := range []string{"", `,"options_mapping":"{}"`} {
			rec := f.do("POST", "/api/v1/webhooks", fmt.Sprintf(`{"job_id":%q,"name":"a","options":{"token":"plain-password"}%s}`, job, m), f.admin)
			assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), "plain-password")
		}
		var list struct {
			Items []api.Webhook `json:"items"`
		}
		f.callJSON(t, "GET", "/api/v1/webhooks?job_id="+job, "", 200, &list)
		assert.Empty(t, list.Items)

		// A mapping may set ordinary options but not secret ones.
		w := f.newHook(t, fmt.Sprintf(`{"job_id":%q,"name":"b","options":{"token":"secret://default/key"},"options_mapping":%q}`, job, `{"env": body.env}`))
		rec := f.doJSON("POST", w.Path, `{"env":"prod"}`)
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		bad := f.newHook(t, fmt.Sprintf(`{"job_id":%q,"name":"c","options_mapping":%q}`, job, `{"token": body.t}`))
		rec = f.doJSON("POST", bad.Path, `{"t":"secret://default/prod-key"}`)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Contains(t, rec.Body.String(), "options.token")
		page, err := st.QueryRuns(t.Context(), store.RunFilter{JobID: job}, "", 10)
		require.NoError(t, err)
		assert.Len(t, page.Runs, 1, "the refused request made no run")
	})
}

func TestWebhookRevokeStopsInFlightRuns(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		w := f.newHook(t, fmt.Sprintf(`{"job_id":%q,"name":"ci"}`, f.jobID))
		f.callJSON(t, "DELETE", "/api/v1/webhooks/"+w.ID, "", 204, nil)
		err := st.WithTx(t.Context(), func(tx store.Tx) error { return tx.TouchWebhook(t.Context(), w.ID, t0) })
		require.ErrorIs(t, err, store.ErrNotFound)
	})
}

func TestWebhookBodyTooLarge(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		w := f.newHook(t, fmt.Sprintf(`{"job_id":%q,"name":"m","options_mapping":%q}`, f.jobID, `{"env": "dev"}`))
		rec := f.doJSON("POST", w.Path, `"`+strings.Repeat("x", 2<<20)+`"`)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	})
}
