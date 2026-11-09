package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

const metricsTimeout = 5 * time.Second

type settingReader interface {
	GetSetting(ctx context.Context, key string) (store.Setting, error)
}

// newMetricsHandler serves /metrics in the Prometheus text format. The numbers
// are read from the database when scraped, so every controller reports the
// same cluster-wide state, plus Go runtime and process metrics of its own.
// The admin token is required: pool names and queue depths are not public.
func newMetricsHandler(ms store.MetricsStore, settings settingReader, log *slog.Logger) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(&dbCollector{ms: ms, log: log})
	inner := promhttp.HandlerFor(reg, promhttp.HandlerOpts{ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn), Timeout: metricsTimeout})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, r, "GET, HEAD")
			return
		}
		if status, msg := checkAdminBearer(r, settings); status != 0 {
			if status == http.StatusUnauthorized {
				w.Header().Set("WWW-Authenticate", `Bearer realm="damson-admin"`)
			}
			http.Error(w, msg, status)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		inner.ServeHTTP(w, r)
	})
}

// checkAdminBearer validates the admin token of a plain HTTP request. It
// returns 0 when the token is valid.
func checkAdminBearer(r *http.Request, settings settingReader) (int, string) {
	if settings == nil {
		return http.StatusServiceUnavailable, "a required dependency is not available"
	}
	h := r.Header.Get("Authorization")
	tok := ""
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		tok = strings.TrimSpace(h[7:])
	}
	if !strings.HasPrefix(tok, tokens.AdminPrefix) {
		return http.StatusUnauthorized, "an admin token is required"
	}
	s, err := settings.GetSetting(r.Context(), tokens.AdminSettingKey)
	switch {
	case errors.Is(err, store.ErrNotFound) || (err == nil && s.Value == ""):
		return http.StatusUnauthorized, "the admin token is not valid"
	case err != nil:
		return http.StatusServiceUnavailable, "a required dependency is not available"
	}
	if subtle.ConstantTimeCompare([]byte(tokens.Hash(tok)), []byte(s.Value)) != 1 {
		return http.StatusUnauthorized, "the admin token is not valid"
	}
	return 0, ""
}

var (
	descRuns       = prometheus.NewDesc("damson_runs", "Runs by status, cluster-wide.", []string{"status"}, nil)
	descQueue      = prometheus.NewDesc("damson_queue_depth", "Executions waiting for an agent, by pool.", []string{"pool"}, nil)
	descActive     = prometheus.NewDesc("damson_executions_active", "Executions dispatched or running, by pool.", []string{"pool", "state"}, nil)
	descAgents     = prometheus.NewDesc("damson_agents_connected", "Connected agents, by pool.", []string{"pool"}, nil)
	descSlots      = prometheus.NewDesc("damson_agent_slots", "Execution slots of the connected agents, by pool.", []string{"pool"}, nil)
	descLogDeletes = prometheus.NewDesc("damson_log_object_deletes_pending", "Queued deletes of external log objects.", nil, nil)
	descScrapeOK   = prometheus.NewDesc("damson_metrics_database_up", "1 when the database answered the scrape.", nil, nil)
)

type dbCollector struct {
	ms  store.MetricsStore
	log *slog.Logger
}

func (c *dbCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{descRuns, descQueue, descActive, descAgents, descSlots, descLogDeletes, descScrapeOK} {
		ch <- d
	}
}

func (c *dbCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTimeout)
	defer cancel()
	snap, err := c.ms.MetricsSnapshot(ctx)
	if err != nil {
		c.log.Warn("metrics: reading the database failed", "error", err)
		ch <- prometheus.MustNewConstMetric(descScrapeOK, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(descScrapeOK, prometheus.GaugeValue, 1)
	for status, n := range snap.RunsByStatus {
		ch <- prometheus.MustNewConstMetric(descRuns, prometheus.GaugeValue, float64(n), status)
	}
	for _, e := range snap.Executions {
		if e.State == "queued" {
			ch <- prometheus.MustNewConstMetric(descQueue, prometheus.GaugeValue, float64(e.Count), e.Pool)
			continue
		}
		ch <- prometheus.MustNewConstMetric(descActive, prometheus.GaugeValue, float64(e.Count), e.Pool, e.State)
	}
	for _, a := range snap.Agents {
		ch <- prometheus.MustNewConstMetric(descAgents, prometheus.GaugeValue, float64(a.Agents), a.Pool)
		ch <- prometheus.MustNewConstMetric(descSlots, prometheus.GaugeValue, float64(a.Slots), a.Pool)
	}
	ch <- prometheus.MustNewConstMetric(descLogDeletes, prometheus.GaugeValue, float64(snap.LogDeletesPending))
}
