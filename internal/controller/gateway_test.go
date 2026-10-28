package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

func TestAuthRejectedBeforeUpgrade(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		r := newRig(t, st, func(c *Config) { c.InsecureDev = false; c.TrustForwardedProto = true })
		ctx := context.Background()

		revoked, hash, _ := tokens.New()
		tok, err := st.CreatePoolToken(ctx, r.pool.ID, "revoked", hash, t0)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.RevokePoolToken(ctx, tok.ID, t0); err != nil {
			t.Fatal(err)
		}

		good := "Bearer " + r.token
		tests := []struct {
			name   string
			header http.Header
			want   int
		}{
			{"plain ws without insecure-dev", http.Header{"Authorization": {good}, "X-Damson-Agent-Id": {"a1"}}, http.StatusForbidden},
			{"no token", http.Header{"X-Forwarded-Proto": {"https"}, "X-Damson-Agent-Id": {"a1"}}, http.StatusUnauthorized},
			{"not a bearer", http.Header{"X-Forwarded-Proto": {"https"}, "Authorization": {"Basic abc"}, "X-Damson-Agent-Id": {"a1"}}, http.StatusUnauthorized},
			{"unknown token", http.Header{"X-Forwarded-Proto": {"https"}, "Authorization": {"Bearer dmp_nope"}, "X-Damson-Agent-Id": {"a1"}}, http.StatusUnauthorized},
			{"revoked token", http.Header{"X-Forwarded-Proto": {"https"}, "Authorization": {"Bearer " + revoked}, "X-Damson-Agent-Id": {"a1"}}, http.StatusUnauthorized},
			{"missing agent id", http.Header{"X-Forwarded-Proto": {"https"}, "Authorization": {good}}, http.StatusBadRequest},
			{"agent id with space", http.Header{"X-Forwarded-Proto": {"https"}, "Authorization": {good}, "X-Damson-Agent-Id": {"a b"}}, http.StatusBadRequest},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				a, resp, err := r.tryDial("a1", tt.header)
				if err == nil {
					a.close()
					t.Fatal("dial succeeded; want rejection before upgrade")
				}
				if resp == nil || resp.StatusCode != tt.want {
					t.Fatalf("status = %v, want %d (err %v)", resp, tt.want, err)
				}
				if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
					t.Errorf("Content-Type = %q, want problem+json", ct)
				}
				body, _ := io.ReadAll(resp.Body)
				var p struct {
					Status int `json:"status"`
				}
				if json.Unmarshal(body, &p) != nil || p.Status != tt.want {
					t.Errorf("body = %s", body)
				}
				if strings.Contains(string(body), r.token) || strings.Contains(string(body), revoked) {
					t.Error("response body leaks a token")
				}
			})
		}
		if got := r.gw.Connected(); len(got) != 0 {
			t.Errorf("connected = %v, want none", got)
		}
	})
}

func TestForwardedProtoIgnoredUnlessTrusted(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		r := newRig(t, st, func(c *Config) { c.InsecureDev = false })
		_, resp, err := r.tryDial("a1", http.Header{
			"X-Forwarded-Proto": {"https"}, "Authorization": {"Bearer " + r.token}, "X-Damson-Agent-Id": {"a1"},
		})
		if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("forged X-Forwarded-Proto: status %v err %v, want 403", resp, err)
		}
	})
}

func TestForwardedHTTPSAllowedWhenTrusted(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		r := newRig(t, st, func(c *Config) { c.InsecureDev = false; c.TrustForwardedProto = true })
		a, _, err := r.tryDial("a1", http.Header{
			"X-Forwarded-Proto": {"https"}, "Authorization": {"Bearer " + r.token}, "X-Damson-Agent-Id": {"a1"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.hello(defaultHello(1)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestHelloRegistersAgent(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		r := newRig(t, st)
		a := r.dial("agent-1")
		req := defaultHello(1)
		req.Executions = []protocol.ExecutionState{{AttemptID: "att-1", State: protocol.ExecRunning}}
		resp, err := a.hello(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Protocol != protocol.ProtocolVersion || resp.HeartbeatIntervalMS != 10000 ||
			resp.Limits.MaxFrameBytes != protocol.MaxFrameSize || resp.Limits.LogBatchBytes != 65536 || resp.Limits.LogBatchIntervalMS != 200 {
			t.Errorf("response = %+v", resp)
		}
		if len(resp.Reconciliation) != 1 || resp.Reconciliation[0] != (protocol.Reconciliation{AttemptID: "att-1", Action: protocol.ActionCancel}) {
			t.Errorf("reconciliation = %+v", resp.Reconciliation)
		}
		got, err := st.GetAgent(context.Background(), "agent-1")
		if err != nil {
			t.Fatal(err)
		}
		if got.PoolID != r.pool.ID || got.Version != "1.2.3" || got.Protocol != 1 || got.Capacity != 4 ||
			got.ControllerID != "ctl-1" || got.SessionSeq != 1 || got.Labels["os"] != "linux" {
			t.Errorf("agent row = %+v", got)
		}
		if _, ok := got.Capabilities["bin:git"]; !ok {
			t.Errorf("capabilities = %v", got.Capabilities)
		}
		if ids := r.gw.Connected(); len(ids) != 1 || ids[0] != "agent-1" {
			t.Errorf("connected = %v", ids)
		}
		if _, ok := r.gw.Peer("agent-1"); !ok {
			t.Error("Peer(agent-1) not found")
		}
	})
}

func TestHelloRejections(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		r := newRig(t, st)
		tests := []struct {
			name string
			mod  func(*protocol.HelloRequest)
			code int
		}{
			{"future protocol", func(h *protocol.HelloRequest) { h.Protocol = 2 }, protocol.CodeUnsupportedProtocol},
			{"protocol zero", func(h *protocol.HelloRequest) { h.Protocol = 0 }, protocol.CodeUnsupportedProtocol},
			{"no session_seq", func(h *protocol.HelloRequest) { h.SessionSeq = 0 }, protocol.CodeInvalidParams},
			{"no capacity", func(h *protocol.HelloRequest) { h.MaxConcurrent = 0 }, protocol.CodeInvalidParams},
			{"wrong pool", func(h *protocol.HelloRequest) { h.Pool = "other" }, protocol.CodeInvalidParams},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				a := r.dial("agent-x")
				req := defaultHello(1)
				tt.mod(&req)
				_, err := a.hello(req)
				var re *protocol.RPCError
				if !errors.As(err, &re) || re.Code != tt.code {
					t.Fatalf("err = %v, want RPC error %d", err, tt.code)
				}
				select {
				case <-a.runDone: // the controller closes the socket after a refused hello
				case <-time.After(3 * time.Second):
					t.Fatal("socket stayed open after a refused hello")
				}
			})
		}
		if _, err := st.GetAgent(context.Background(), "agent-x"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("rejected agent persisted: %v", err)
		}
	})
}

func TestHelloTimeout(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		r := newRig(t, st, func(c *Config) { c.HelloTimeout = 50 * time.Millisecond })
		a := r.dial("silent")
		select {
		case <-a.runDone:
		case <-time.After(3 * time.Second):
			t.Fatal("socket without hello was not closed")
		}
	})
}

func TestSupersedeOlderSocket(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		r := newRig(t, st)
		first := r.dial("agent-1")
		if _, err := first.hello(defaultHello(1)); err != nil {
			t.Fatal(err)
		}
		second := r.dial("agent-1")
		if _, err := second.hello(defaultHello(2)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-first.runDone:
		case <-time.After(3 * time.Second):
			t.Fatal("older socket was not closed")
		}
		got, err := st.GetAgent(context.Background(), "agent-1")
		if err != nil || got.SessionSeq != 2 || got.ControllerID != "ctl-1" || got.DisconnectedAt != nil {
			t.Fatalf("agent = %+v, %v; want session 2 owned and connected", got, err)
		}
		if ids := r.gw.Connected(); len(ids) != 1 {
			t.Errorf("connected = %v", ids)
		}

		// A hello with an older session_seq must not take the agent back.
		stale := r.dial("agent-1")
		_, err = stale.hello(defaultHello(1))
		var re *protocol.RPCError
		if !errors.As(err, &re) || re.Code != protocol.CodeStaleSession {
			t.Fatalf("stale hello err = %v", err)
		}
		got, _ = st.GetAgent(context.Background(), "agent-1")
		if got.SessionSeq != 2 {
			t.Errorf("session_seq = %d, want 2", got.SessionSeq)
		}
	})
}

func TestDisconnectMarksAgent(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		r := newRig(t, st)
		a := r.dial("agent-1")
		if _, err := a.hello(defaultHello(1)); err != nil {
			t.Fatal(err)
		}
		a.close()
		eventually(t, "agent marked disconnected", func() bool {
			got, err := st.GetAgent(context.Background(), "agent-1")
			return err == nil && got.DisconnectedAt != nil && got.ControllerID == ""
		})
		eventually(t, "registry empty", func() bool { return len(r.gw.Connected()) == 0 })
	})
}

func TestHeartbeatTelemetryIsBatched(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		r := newRig(t, st)
		a := r.dial("agent-1")
		if _, err := a.hello(defaultHello(1)); err != nil {
			t.Fatal(err)
		}
		before, _ := st.GetAgent(context.Background(), "agent-1")

		hbAt := r.clock.advance(3 * time.Second) // heartbeats are stamped when received
		for i := range 3 {
			a.heartbeat(protocol.Heartbeat{RunningAttempts: []string{"att-1"}, FreeSlots: 3 - i, CPUPercent: 12.5, MemoryBytes: 1 << 20, Load1: 0.5})
		}
		time.Sleep(100 * time.Millisecond) // no flush tick yet: nothing may be written
		mid, _ := st.GetAgent(context.Background(), "agent-1")
		if !mid.LastSeen.Equal(before.LastSeen) || string(mid.Telemetry) != string(before.Telemetry) {
			t.Fatalf("telemetry written before the flush tick: %+v", mid)
		}

		r.clock.advance(DefaultFlushInterval)
		eventually(t, "batched telemetry", func() bool {
			got, err := st.GetAgent(context.Background(), "agent-1")
			return err == nil && got.LastSeen.Equal(hbAt) && len(got.Telemetry) > 0
		})
		got, _ := st.GetAgent(context.Background(), "agent-1")
		var hb protocol.Heartbeat
		if err := json.Unmarshal(got.Telemetry, &hb); err != nil {
			t.Fatal(err)
		}
		if hb.FreeSlots != 1 || hb.CPUPercent != 12.5 || len(hb.RunningAttempts) != 1 {
			t.Errorf("stored telemetry = %+v, want the last heartbeat", hb)
		}
	})
}

func TestControllerInstanceRegisteredAndHeartbeated(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		r := newRig(t, st)
		list, err := st.ListControllerInstances(context.Background())
		if err != nil || len(list) != 1 || list[0].ID != "ctl-1" || list[0].InternalURL != "http://ctl-1:8080" {
			t.Fatalf("instances = %+v, %v", list, err)
		}
		now := r.clock.advance(DefaultInstanceInterval)
		eventually(t, "controller heartbeat", func() bool {
			l, err := st.ListControllerInstances(context.Background())
			return err == nil && len(l) == 1 && l[0].LastSeen.Equal(now)
		})
		// A vanished row is re-registered by the next heartbeat.
		if err := st.DeleteControllerInstance(context.Background(), "ctl-1"); err != nil {
			t.Fatal(err)
		}
		r.clock.advance(DefaultInstanceInterval)
		eventually(t, "re-registration", func() bool {
			l, err := st.ListControllerInstances(context.Background())
			return err == nil && len(l) == 1
		})
	})
}

func TestStopSendsReconnectAndDeregisters(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		r := newRig(t, st)
		agents := []*agent{r.dial("a1"), r.dial("a2")}
		for i, a := range agents {
			if _, err := a.hello(defaultHello(uint64(i + 1))); err != nil {
				t.Fatal(err)
			}
		}
		agents[0].heartbeat(protocol.Heartbeat{FreeSlots: 2})
		eventually(t, "two live agents", func() bool { return len(r.gw.Connected()) == 2 })
		time.Sleep(50 * time.Millisecond) // let the heartbeat reach the session

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.gw.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		for _, a := range agents {
			select {
			case rc := <-a.reconnCh:
				if rc.JitterMS != 1500 || rc.Reason == "" {
					t.Errorf("%s reconnect = %+v", a.id, rc)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("%s got no agent.reconnect", a.id)
			}
			<-a.runDone
		}
		if l, _ := st.ListControllerInstances(context.Background()); len(l) != 0 {
			t.Errorf("instances after Stop = %+v", l)
		}
		// The final batch was written before the sockets closed.
		got, _ := st.GetAgent(context.Background(), "a1")
		if len(got.Telemetry) == 0 {
			t.Error("last telemetry was not flushed on Stop")
		}
		// New connections are refused after Stop.
		if _, resp, err := r.tryDial("a3", http.Header{"Authorization": {"Bearer " + r.token}, "X-Damson-Agent-Id": {"a3"}}); err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("dial after Stop: status %v err %v, want 503", resp, err)
		}
		if err := r.gw.Stop(ctx); err != nil {
			t.Errorf("second Stop: %v", err)
		}
	})
}

// TestTokenNeverLogged proves the pool token (and its hash) stay out of the
// logs on every path: accepted, rejected and shutdown.
func TestTokenNeverLogged(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		r := newRig(t, st)
		a := r.dial("agent-1")
		if _, err := a.hello(defaultHello(1)); err != nil {
			t.Fatal(err)
		}
		a.heartbeat(protocol.Heartbeat{FreeSlots: 1})
		bad := "dmp_wrongtoken"
		_, _, _ = r.tryDial("bad", http.Header{"Authorization": {"Bearer " + bad}, "X-Damson-Agent-Id": {"bad"}})
		r.advanceAll()
		r.stop()

		logs := r.logs.String()
		if logs == "" {
			t.Fatal("no logs captured")
		}
		for _, secret := range []string{r.token, tokens.Hash(r.token), bad, tokens.Hash(bad)} {
			if strings.Contains(logs, secret) {
				t.Errorf("logs contain a token or token hash:\n%s", logs)
			}
		}
		if !strings.Contains(logs, `"agent_id":"agent-1"`) {
			t.Error("logs lack the agent_id attribute")
		}
	})
}

func (r *rig) advanceAll() {
	r.clock.advance(DefaultFlushInterval)
	r.clock.advance(DefaultInstanceInterval)
}

func TestHelloPoolConflict(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		r := newRig(t, st)
		first := r.dial("agent-1")
		if _, err := first.hello(defaultHello(1)); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		other, err := st.CreatePool(ctx, "other", "", t0)
		if err != nil {
			t.Fatal(err)
		}
		plain, hash, _ := tokens.New()
		if _, err := st.CreatePoolToken(ctx, other.ID, "t", hash, t0); err != nil {
			t.Fatal(err)
		}
		a, _, err := r.tryDial("agent-1", http.Header{"Authorization": {"Bearer " + plain}, "X-Damson-Agent-Id": {"agent-1"}})
		if err != nil {
			t.Fatal(err)
		}
		req := defaultHello(2)
		req.Pool = "other"
		_, err = a.hello(req)
		var re *protocol.RPCError
		if !errors.As(err, &re) || re.Code != protocol.CodePoolConflict {
			t.Fatalf("err = %v, want CodePoolConflict", err)
		}
	})
}

func TestSecondHelloOnSocketRefused(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		r := newRig(t, st)
		a := r.dial("agent-1")
		if _, err := a.hello(defaultHello(1)); err != nil {
			t.Fatal(err)
		}
		_, err := a.hello(defaultHello(2))
		var re *protocol.RPCError
		if !errors.As(err, &re) || re.Code != protocol.CodeInvalidRequest {
			t.Fatalf("second hello err = %v", err)
		}
	})
}

// TestConcurrentHellosKeepHighestSeq races hellos for one agent on separate
// sockets; whatever the interleaving, the registry and the database must end
// on the highest session_seq. Run with -race.
func TestConcurrentHellosKeepHighestSeq(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		r := newRig(t, st)
		const n = 6
		agents := make([]*agent, n)
		for i := range agents {
			agents[i] = r.dial("agent-1")
		}
		var wg sync.WaitGroup
		for i, a := range agents {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = a.hello(defaultHello(uint64(i + 1)))
			}()
		}
		wg.Wait()
		eventually(t, "only the highest session left", func() bool {
			got, err := st.GetAgent(context.Background(), "agent-1")
			if err != nil || got.SessionSeq != n || got.DisconnectedAt != nil {
				return false
			}
			p, ok := r.gw.Peer("agent-1")
			return ok && p == r.gw.reg.get("agent-1").peer && r.gw.reg.get("agent-1").seqValue() == n
		})
		for i, a := range agents[:n-1] {
			select {
			case <-a.runDone:
			case <-time.After(3 * time.Second):
				t.Fatalf("lower session %d stayed open", i+1)
			}
		}
	})
}
