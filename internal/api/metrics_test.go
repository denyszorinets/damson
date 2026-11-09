package api_test

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

type snapCounter struct {
	store.Store
	snapshots atomic.Int32
}

func (c *snapCounter) MetricsSnapshot(ctx context.Context, seenAfter time.Time) (store.MetricsSnapshot, error) {
	c.snapshots.Add(1)
	return c.Store.MetricsSnapshot(ctx, seenAfter)
}

func metricsServer(t *testing.T, st store.Store, probe func(context.Context) error) (http.Handler, string, *snapCounter) {
	t.Helper()
	tok, hash, err := tokens.GenerateWithPrefix(rand.Reader, tokens.MetricsPrefix)
	require.NoError(t, err)
	_, err = st.CreateSetting(t.Context(), tokens.MetricsSettingKey, hash)
	require.NoError(t, err)
	cs := &snapCounter{Store: st}
	h := api.NewServer(api.Config{
		Addr: "127.0.0.1:0", Store: st, Data: st, Metrics: cs, LogProbe: probe, UI: ui(),
		Logger: slog.New(slog.NewJSONHandler(&syncBuffer{}, nil)),
	}).Handler()
	return h, tok, cs
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

func TestMetricsNeedTheMetricsToken(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		admin, ahash, err := tokens.GenerateWithPrefix(rand.Reader, tokens.AdminPrefix)
		require.NoError(t, err)
		_, err = st.CreateSetting(t.Context(), tokens.AdminSettingKey, ahash)
		require.NoError(t, err)
		h, tok, cs := metricsServer(t, st, nil)
		pool, _, _ := tokens.New()
		for _, bad := range []string{"", pool, admin, tokens.MetricsPrefix + "nope"} {
			rec := get(h, "/metrics", bad)
			assert.Equal(t, http.StatusUnauthorized, rec.Code, "token %q", bad)
			assert.NotEmpty(t, rec.Header().Get("WWW-Authenticate"))
			assert.Equal(t, api.ProblemContentType, rec.Header().Get("Content-Type"))
		}
		assert.Zero(t, cs.snapshots.Load(), "a refused scrape must not query the database")

		rec := get(h, "/metrics", tok)
		require.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()
		assert.Contains(t, body, "damson_metrics_database_up 1")
		assert.Contains(t, body, "damson_log_object_deletes_pending 0")
		assert.Contains(t, body, "go_goroutines")
		assert.NotContains(t, body, tok)
		assert.NotContains(t, body, "damson_log_store_up", "no external backend, no gauge")

		// The metrics token reads nothing else.
		assert.Equal(t, http.StatusUnauthorized, get(h, "/api/v1/secrets", tok).Code)

		// Revoking ends access at once.
		cur, err := st.GetSetting(t.Context(), tokens.MetricsSettingKey)
		require.NoError(t, err)
		_, err = st.UpdateSetting(t.Context(), tokens.MetricsSettingKey, "", cur.Version)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, get(h, "/metrics", tok).Code)
	})
}

func TestMetricsOffWithoutAStore(t *testing.T) {
	h := api.NewServer(api.Config{Addr: "127.0.0.1:0", Store: fakeStore{}, UI: ui(), Logger: slog.New(slog.NewJSONHandler(&syncBuffer{}, nil))}).Handler()
	assert.NotContains(t, get(h, "/metrics", "").Body.String(), "damson_metrics_database_up")
}

func TestMetricsReportTheLogStore(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{{"down", errors.New("bucket gone"), "damson_log_store_up 0"}, {"up", nil, "damson_log_store_up 1"}} {
		t.Run(tc.name, func(t *testing.T) {
			forEachBackend(t, func(t *testing.T, st store.Store) {
				h, tok, _ := metricsServer(t, st, func(context.Context) error { return tc.err })
				body := get(h, "/metrics", tok).Body.String()
				assert.Contains(t, body, tc.want)
				assert.NotContains(t, body, "bucket gone")
				assert.Equal(t, http.StatusOK, get(h, "/readyz", "").Code, "a log store outage must not make the controller unready")
			})
		})
	}
}
