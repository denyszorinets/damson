package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/denyszorinets/damson/internal/agent"
	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
	"github.com/denyszorinets/damson/internal/version"
	"github.com/denyszorinets/damson/plugins/all"
)

// agentStopTimeout bounds how long the agent may take to close its socket.
const agentStopTimeout = 5 * time.Second

// devTokenName names the pool token that dev mode issues to its own agent.
const devTokenName = "dev-agent"

// newAgent builds an agent from the validated agent settings.
func newAgent(cfg config.Agent, controllerURL, token string, log *slog.Logger) (*agent.Agent, error) {
	id, err := agent.NewAgentID()
	if err != nil {
		return nil, err
	}
	return agent.New(agent.Config{
		ControllerURL: controllerURL,
		Token:         token,
		AgentID:       id,
		Pool:          cfg.Pool,
		Slots:         cfg.Slots,
		Version:       version.Version,
		Plugins:       all.Plugins(),
		Logger:        log,
	})
}

// runAgent connects to the controller and blocks until ctx is cancelled or
// the controller refuses the agent for good (bad token, pool conflict).
func runAgent(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	a, err := newAgent(cfg.Agent, cfg.Agent.ControllerURL, cfg.Agent.Token, log)
	if err != nil {
		return err
	}
	if err := a.Start(ctx); err != nil {
		return err
	}
	var fatal error
	select {
	case <-ctx.Done():
		log.Info("stopping")
	case <-a.Done():
		fatal = a.Err()
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentStopTimeout)
	defer cancel()
	return errors.Join(fatal, a.Stop(sctx))
}

// startDevAgent provisions the pool and a fresh pool token in the dev store
// and starts an agent against the controller listening on addr. The token is
// handed straight to the agent: it is never printed or logged, and tokens
// from earlier runs are revoked so the dev database does not accumulate them.
func startDevAgent(ctx context.Context, st store.Store, cfg config.Config, addr string, log *slog.Logger) (*agent.Agent, error) {
	now := time.Now()
	pool, err := st.GetPoolByName(ctx, cfg.Agent.Pool)
	if errors.Is(err, store.ErrNotFound) {
		pool, err = st.CreatePool(ctx, cfg.Agent.Pool, "created by damson dev", now)
	}
	if err != nil {
		return nil, fmt.Errorf("provisioning dev pool: %w", err)
	}
	if err := revokeDevTokens(ctx, st, pool.ID, now); err != nil {
		return nil, err
	}
	plain, hash, err := tokens.New()
	if err != nil {
		return nil, err
	}
	if _, err := st.CreatePoolToken(ctx, pool.ID, devTokenName, hash, now); err != nil {
		return nil, fmt.Errorf("creating dev pool token: %w", err)
	}
	url, err := loopbackURL(addr)
	if err != nil {
		return nil, err
	}
	a, err := newAgent(cfg.Agent, url, plain, log)
	if err != nil {
		return nil, err
	}
	if err := a.Start(ctx); err != nil {
		return nil, err
	}
	return a, nil
}

func revokeDevTokens(ctx context.Context, st store.Store, poolID string, now time.Time) error {
	ts, err := st.ListPoolTokens(ctx, poolID)
	if err != nil {
		return fmt.Errorf("listing dev pool tokens: %w", err)
	}
	for _, t := range ts {
		if t.Name != devTokenName || t.RevokedAt != nil {
			continue
		}
		if err := st.RevokePoolToken(ctx, t.ID, now); err != nil && !errors.Is(err, store.ErrStale) {
			return fmt.Errorf("revoking old dev token: %w", err)
		}
	}
	return nil
}

// loopbackURL turns the bound listener address into the ws:// URL the
// in-process agent dials: wildcard hosts become loopback.
func loopbackURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("parsing listen address %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "ws://" + net.JoinHostPort(host, port), nil
}

// stopDevAgent stops the in-process agent before the controller goes away so
// it does not chase a half-stopped gateway.
func stopDevAgent(ctx context.Context, a *agent.Agent, log *slog.Logger) error {
	if a == nil {
		return nil
	}
	if err := a.Stop(ctx); err != nil {
		log.Warn("stopping dev agent", "error", err)
		return err
	}
	return nil
}
