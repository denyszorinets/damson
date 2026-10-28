package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/store"
)

func TestPluginConfig(t *testing.T) {
	if got := pluginConfig(config.Agent{}); got != nil {
		t.Errorf("default plugin config = %v, want none (safe default)", got)
	}
	got := pluginConfig(config.Agent{AllowInsecureHostKeys: true})
	if got["ssh"]["allow_insecure_host_keys"] != "true" {
		t.Errorf("plugin config = %v", got)
	}
}

// waitAgents polls the store until pred holds for the listed agents.
func waitAgents(t *testing.T, st store.Store, what string, pred func([]store.Agent) bool) []store.Agent {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		page, err := st.ListAgents(context.Background(), "", 100)
		if err == nil && pred(page.Agents) {
			return page.Agents
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
	return nil
}

func stopRole(t *testing.T, cancel context.CancelFunc, done <-chan int, errb *syncBuf) {
	t.Helper()
	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			t.Errorf("exit = %d; log: %s", code, errb.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("did not shut down")
	}
}

// TestDevRunsConnectedAgent checks that `damson dev` starts the controller
// and an agent in one process and that the agent registers itself.
func TestDevRunsConnectedAgent(t *testing.T) {
	be := adminBackends()[0] // dev is SQLite-only
	env := append(adminEnv(t, be, false), "DAMSON_CONTROLLER__LISTEN=127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var errb syncBuf
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"dev"}, io.Discard, &errb, env) }()
	listenAddr(t, &errb)

	st := openStore(t, be, env)
	agents := waitAgents(t, st, "dev agent to connect", func(as []store.Agent) bool {
		return len(as) == 1 && as[0].ControllerID != "" && as[0].DisconnectedAt == nil
	})
	if agents[0].Capacity < 1 || agents[0].Labels["os"] == "" {
		t.Errorf("agent row = %+v", agents[0])
	}
	pool, err := st.GetPoolByName(context.Background(), "default")
	if err != nil || agents[0].PoolID != pool.ID {
		t.Errorf("agent is not in the default pool: %v", err)
	}
	stopRole(t, cancel, done, &errb)
	if strings.Contains(errb.String(), "dmp_") {
		t.Errorf("a pool token reached the log: %s", errb.String())
	}
	if !strings.Contains(errb.String(), `"agent_id"`) {
		t.Errorf("agent logs lack agent_id: %s", errb.String())
	}
}

// TestDevRestartRevokesOldToken checks that restarting dev reuses the
// database without piling up active dev tokens.
func TestDevRestartRevokesOldToken(t *testing.T) {
	be := adminBackends()[0]
	env := append(adminEnv(t, be, false), "DAMSON_CONTROLLER__LISTEN=127.0.0.1:0")
	for range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		var errb syncBuf
		done := make(chan int, 1)
		go func() { done <- run(ctx, []string{"dev"}, io.Discard, &errb, env) }()
		listenAddr(t, &errb)
		st := openStore(t, be, env)
		waitAgents(t, st, "dev agent to connect", func(as []store.Agent) bool {
			for _, a := range as {
				if a.ControllerID != "" && a.DisconnectedAt == nil {
					return true
				}
			}
			return false
		})
		stopRole(t, cancel, done, &errb)
	}
	st := openStore(t, be, env)
	pool, err := st.GetPoolByName(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	ts, err := st.ListPoolTokens(context.Background(), pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, tok := range ts {
		if tok.RevokedAt == nil {
			active++
		}
	}
	if len(ts) != 2 || active != 1 {
		t.Errorf("tokens = %d total, %d active; want 2 and 1", len(ts), active)
	}
}

// TestAgentRoleConnects runs a controller and a separate `damson agent`.
func TestAgentRoleConnects(t *testing.T) {
	forEachBackend(t, func(t *testing.T, be adminBackend) {
		env := append(adminEnv(t, be, true), "DAMSON_CONTROLLER__LISTEN=127.0.0.1:0")
		mustAdmin(t, env, "pool", "create", "default")
		token := strings.TrimSpace(mustAdmin(t, env, "token", "create", "--pool", "default", "--name", "t"))

		cctx, ccancel := context.WithCancel(context.Background())
		defer ccancel()
		var cerr syncBuf
		cdone := make(chan int, 1)
		go func() { cdone <- run(cctx, []string{"controller", "-insecure-dev"}, io.Discard, &cerr, env) }()
		addr := listenAddr(t, &cerr)

		actx, acancel := context.WithCancel(context.Background())
		defer acancel()
		var aerr syncBuf
		adone := make(chan int, 1)
		aenv := append(append([]string(nil), env...),
			"DAMSON_AGENT__CONTROLLER_URL=ws://"+addr, "DAMSON_AGENT__TOKEN="+token, "DAMSON_AGENT__SLOTS=2")
		go func() { adone <- run(actx, []string{"agent"}, io.Discard, &aerr, aenv) }()

		st := openStore(t, be, env)
		agents := waitAgents(t, st, "agent to connect", func(as []store.Agent) bool {
			return len(as) == 1 && as[0].ControllerID != ""
		})
		if agents[0].Capacity != 2 {
			t.Errorf("capacity = %d, want 2", agents[0].Capacity)
		}
		stopRole(t, acancel, adone, &aerr)
		stopRole(t, ccancel, cdone, &cerr)
		if strings.Contains(aerr.String(), token) || strings.Contains(cerr.String(), token) {
			t.Errorf("pool token leaked into logs")
		}
	})
}

// TestAgentRoleFatalOnBadToken checks the agent exits instead of retrying
// forever when the controller rejects its token.
func TestAgentRoleFatalOnBadToken(t *testing.T) {
	be := adminBackends()[0]
	env := append(adminEnv(t, be, true), "DAMSON_CONTROLLER__LISTEN=127.0.0.1:0")
	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	var cerr syncBuf
	cdone := make(chan int, 1)
	go func() { cdone <- run(cctx, []string{"controller", "-insecure-dev"}, io.Discard, &cerr, env) }()
	addr := listenAddr(t, &cerr)

	var aerr, aout strings.Builder
	aenv := []string{"DAMSON_AGENT__CONTROLLER_URL=ws://" + addr, "DAMSON_AGENT__TOKEN=dmp_not-a-real-token"}
	code := run(context.Background(), []string{"agent"}, &aout, &aerr, aenv)
	if code != exitError {
		t.Errorf("exit = %d, want %d; stderr: %s", code, exitError, aerr.String())
	}
	if !strings.Contains(aerr.String(), "401") || strings.Contains(aerr.String(), "dmp_not-a-real-token") {
		t.Errorf("stderr = %q", aerr.String())
	}
	stopRole(t, ccancel, cdone, &cerr)
}
