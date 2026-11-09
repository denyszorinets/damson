package api_test

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

func metricsServer(t *testing.T, st store.Store, probe func(context.Context) error) (http.Handler, string) {
	t.Helper()
	admin, hash, err := tokens.GenerateWithPrefix(rand.Reader, tokens.AdminPrefix)
	require.NoError(t, err)
	if _, err = st.CreateSetting(t.Context(), tokens.AdminSettingKey, hash); !errors.Is(err, store.ErrConflict) {
		require.NoError(t, err)
	}
	h := api.NewServer(api.Config{
		Addr: "127.0.0.1:0", Store: st, Data: st, Metrics: st, LogProbe: probe, UI: ui(),
		Logger: slog.New(slog.NewJSONHandler(&syncBuffer{}, nil)),
	}).Handler()
	return h, admin
}

func get(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMetricsNeedTheAdminToken(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		h, admin := metricsServer(t, st, nil)
		pool, _, _ := tokens.New()
		for _, tok := range []string{"", pool, tokens.AdminPrefix + "nope"} {
			rec := get(h, "/metrics", tok)
			assert.Equal(t, http.StatusUnauthorized, rec.Code, "token %q", tok)
			assert.NotEmpty(t, rec.Header().Get("WWW-Authenticate"))
		}
		rec := get(h, "/metrics", admin)
		require.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()
		assert.Contains(t, body, "damson_metrics_database_up 1")
		assert.Contains(t, body, "damson_log_object_deletes_pending 0")
		assert.Contains(t, body, "go_goroutines")
		assert.NotContains(t, body, admin)
	})
}

func TestMetricsOffWithoutAStore(t *testing.T) {
	h := api.NewServer(api.Config{Addr: "127.0.0.1:0", Store: fakeStore{}, UI: ui(), Logger: slog.New(slog.NewJSONHandler(&syncBuffer{}, nil))}).Handler()
	assert.NotContains(t, get(h, "/metrics", "").Body.String(), "damson_metrics_database_up")
}

func TestReadyzChecksTheLogStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		fail := errors.New("bucket gone")
		h, _ := metricsServer(t, st, func(context.Context) error { return fail })
		rec := get(h, "/readyz", "")
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.NotContains(t, rec.Body.String(), "bucket gone", "the cause is logged, not returned")

		ok, _ := metricsServer(t, st, func(context.Context) error { return nil })
		assert.Equal(t, http.StatusOK, get(ok, "/readyz", "").Code)
	})
}
