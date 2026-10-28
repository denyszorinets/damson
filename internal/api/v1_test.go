package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// v1Fixture seeds pools "alpha", "beta", "gamma" and agents a1 (connected,
// with telemetry), a2 (disconnected) and a3 (connected) in alpha. It returns
// a handler, the log buffer and the pool token plaintext and hash.
func v1Fixture(t *testing.T, st store.Store) (h http.Handler, logs *bytes.Buffer, plain, hash string) {
	t.Helper()
	ctx := context.Background()
	var alpha store.Pool
	for _, name := range []string{"alpha", "beta", "gamma"} {
		p, err := st.CreatePool(ctx, name, "pool "+name, t0)
		if err != nil {
			t.Fatal(err)
		}
		if name == "alpha" {
			alpha = p
		}
	}
	plain, hash, err := tokens.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePoolToken(ctx, alpha.ID, "t1", hash, t0); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"a1", "a2", "a3"} {
		err := st.UpsertAgentOnHello(ctx, store.AgentHello{
			ID: id, PoolID: alpha.ID, Version: "1.0", Protocol: 1, ControllerID: "c1", SessionSeq: 1,
			Labels: map[string]string{"os": "linux"}, Capabilities: map[string]string{"ssh": "1"}, Capacity: 4,
		}, t0.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.BatchTouchAgents(ctx, "c1", []store.AgentTouch{{
		ID: "a1", SessionSeq: 1, LastSeen: t0.Add(time.Minute), Telemetry: json.RawMessage(`{"cpu":0.5,"mem_mb":128}`),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkAgentDisconnected(ctx, "a2", "c1", 1, t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	logs = &bytes.Buffer{}
	srv := api.NewServer(api.Config{
		Addr: "127.0.0.1:0", Store: st, Data: st, UI: ui(),
		Logger: slog.New(slog.NewJSONHandler(logs, nil)),
	})
	return srv.Handler(), logs, plain, hash
}

func getJSON(t *testing.T, h http.Handler, target string, wantStatus int, out any) *httptest.ResponseRecorder {
	t.Helper()
	rec := do(h, http.MethodGet, target)
	if rec.Code != wantStatus {
		t.Fatalf("GET %s = %d %s; want %d", target, rec.Code, rec.Body.String(), wantStatus)
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("GET %s: decoding %q: %v", target, rec.Body.String(), err)
		}
	}
	return rec
}

type agentList struct {
	Items []struct {
		ID             string         `json:"id"`
		Status         string         `json:"status"`
		Telemetry      map[string]any `json:"telemetry"`
		LastSeen       time.Time      `json:"last_seen"`
		DisconnectedAt *time.Time     `json:"disconnected_at"`
	} `json:"items"`
	NextCursor string `json:"next_cursor"`
}

func TestV1Agents(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		h, _, _, _ := v1Fixture(t, st)

		var all agentList
		getJSON(t, h, "/api/v1/agents", http.StatusOK, &all)
		if len(all.Items) != 3 || all.NextCursor != "" {
			t.Fatalf("list = %+v", all)
		}
		a1, a2 := all.Items[0], all.Items[1]
		if a1.ID != "a1" || a1.Status != "connected" || a1.Telemetry["cpu"] != 0.5 || !a1.LastSeen.Equal(t0.Add(time.Minute)) {
			t.Errorf("a1 = %+v", a1)
		}
		if a2.ID != "a2" || a2.Status != "disconnected" || a2.DisconnectedAt == nil {
			t.Errorf("a2 = %+v", a2)
		}

		// Cursor pagination walks every agent exactly once.
		var seen []string
		target := "/api/v1/agents?limit=2"
		for range 5 {
			var page agentList
			getJSON(t, h, target, http.StatusOK, &page)
			for _, a := range page.Items {
				seen = append(seen, a.ID)
			}
			if page.NextCursor == "" {
				break
			}
			target = "/api/v1/agents?limit=2&cursor=" + page.NextCursor
		}
		if strings.Join(seen, ",") != "a1,a2,a3" {
			t.Errorf("paged ids = %v", seen)
		}

		var one struct {
			ID           string            `json:"id"`
			PoolID       string            `json:"pool_id"`
			Labels       map[string]string `json:"labels"`
			Capabilities map[string]string `json:"capabilities"`
			Telemetry    map[string]any    `json:"telemetry"`
		}
		getJSON(t, h, "/api/v1/agents/a1", http.StatusOK, &one)
		if one.ID != "a1" || one.PoolID == "" || one.Labels["os"] != "linux" || one.Capabilities["ssh"] != "1" || one.Telemetry["mem_mb"] != 128.0 {
			t.Errorf("get a1 = %+v", one)
		}
	})
}

func TestV1Pools(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		h, _, _, _ := v1Fixture(t, st)
		type pools struct {
			Items []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"items"`
			NextCursor string `json:"next_cursor"`
		}
		var p1, p2 pools
		getJSON(t, h, "/api/v1/pools?limit=2", http.StatusOK, &p1)
		if len(p1.Items) != 2 || p1.Items[0].Name != "alpha" || p1.Items[0].Description != "pool alpha" || p1.NextCursor != "beta" {
			t.Fatalf("page 1 = %+v", p1)
		}
		getJSON(t, h, "/api/v1/pools?limit=2&cursor="+p1.NextCursor, http.StatusOK, &p2)
		if len(p2.Items) != 1 || p2.Items[0].Name != "gamma" || p2.NextCursor != "" {
			t.Fatalf("page 2 = %+v", p2)
		}
	})
}

func TestV1Problems(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		h, _, _, _ := v1Fixture(t, st)
		tests := []struct {
			name, target string
			status       int
			typ          string
			hasErrors    bool
		}{
			{"unknown agent", "/api/v1/agents/nope", 404, "urn:damson:problem:not-found", false},
			{"limit too big", "/api/v1/agents?limit=1000", 422, "urn:damson:problem:validation", true},
			{"limit zero", "/api/v1/pools?limit=0", 422, "urn:damson:problem:validation", true},
			{"unknown route", "/api/v1/nope", 404, "urn:damson:problem:not-found", false},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				rec := do(h, http.MethodGet, tc.target)
				if rec.Code != tc.status {
					t.Fatalf("status = %d %s; want %d", rec.Code, rec.Body.String(), tc.status)
				}
				if ct := rec.Header().Get("Content-Type"); ct != api.ProblemContentType {
					t.Errorf("Content-Type = %q", ct)
				}
				var p api.Problem
				if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
					t.Fatal(err)
				}
				if p.Type != tc.typ || p.Status != tc.status || p.Title == "" || (len(p.Errors) > 0) != tc.hasErrors {
					t.Errorf("problem = %+v", p)
				}
				if tc.name != "unknown route" && p.RequestID == "" {
					t.Errorf("no request_id in %s", rec.Body.String())
				}
				if p.RequestID != "" && p.RequestID != rec.Header().Get("X-Request-Id") {
					t.Errorf("request_id %q != header %q", p.RequestID, rec.Header().Get("X-Request-Id"))
				}
			})
		}
	})
}

func TestV1NoDataStore(t *testing.T) {
	srv := api.NewServer(api.Config{Addr: "127.0.0.1:0", Store: fakeStore{version: 1}, Logger: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))})
	rec := do(srv.Handler(), http.MethodGet, "/api/v1/agents")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "urn:damson:problem:unavailable") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

// TestV1NeverLeaksTokens proves a pool token's plaintext and hash appear in
// no typed response, in the OpenAPI document or in the logs (hard rule 5).
func TestV1NeverLeaksTokens(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		h, logs, plain, hash := v1Fixture(t, st)
		for _, target := range []string{
			"/api/v1/agents", "/api/v1/agents/a1", "/api/v1/agents/nope", "/api/v1/pools",
			"/api/v1/openapi.json", "/api/v1/agents?limit=x",
		} {
			body := do(h, http.MethodGet, target).Body.String()
			for _, secret := range []string{plain, hash} {
				if strings.Contains(body, secret) {
					t.Errorf("GET %s leaks a token value", target)
				}
			}
		}
		for _, secret := range []string{plain, hash} {
			if strings.Contains(logs.String(), secret) {
				t.Errorf("logs leak a token value")
			}
		}
	})
}

func TestOpenAPIDocument(t *testing.T) {
	want, err := api.OpenAPIJSON()
	if err != nil {
		t.Fatal(err)
	}
	srv := api.NewServer(api.Config{Addr: "127.0.0.1:0", Store: fakeStore{version: 1}, Logger: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))})
	rec := do(srv.Handler(), http.MethodGet, "/api/v1/openapi.json")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/openapi+json") {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var doc struct {
		OpenAPI string         `json:"openapi"`
		Paths   map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Errorf("openapi = %q; want 3.1.x", doc.OpenAPI)
	}
	for _, p := range []string{"/agents", "/agents/{id}", "/pools"} {
		if _, ok := doc.Paths[p]; !ok {
			t.Errorf("path %s missing from the document", p)
		}
	}

	// The committed copy (the TypeScript client's source) must match; run
	// `make gen` to refresh it.
	got, err := os.ReadFile("../../web/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("web/openapi.json is out of date; run make gen")
	}
}
