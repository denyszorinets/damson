package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/denyszorinets/damson/internal/agent"
	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
	"github.com/denyszorinets/damson/internal/version"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/ansible"
	"github.com/denyszorinets/damson/plugins/buildah"
	"github.com/denyszorinets/damson/plugins/helm"
	"github.com/denyszorinets/damson/plugins/ssh"
	"github.com/denyszorinets/damson/plugins/transfer"
	"github.com/denyszorinets/damson/plugins/winrm"
)

// agentStopTimeout bounds how long the agent may take to close its socket.
const agentStopTimeout = 5 * time.Second

// devTokenName names the pool token that dev mode issues to its own agent.
const devTokenName = "dev-agent"

// newAgent builds an agent from the validated agent settings.
func newAgent(cfg config.Agent, controllerURL, token string, log *slog.Logger, plugins []pluginsdk.Plugin) (*agent.Agent, error) {
	id, err := agent.NewAgentID()
	if err != nil {
		return nil, err
	}
	pluginCfg, err := pluginConfig(cfg)
	if err != nil {
		return nil, err
	}
	client, err := controllerHTTPClient(cfg)
	if err != nil {
		return nil, err
	}
	return agent.New(agent.Config{
		HTTPClient:    client,
		ControllerURL: controllerURL,
		Token:         token,
		AgentID:       id,
		Pool:          cfg.Pool,
		Slots:         cfg.Slots,
		Version:       version.Version,
		Plugins:       plugins,
		Logger:        log,
		ExecConfig: agent.ExecConfig{
			SpoolDir:     cfg.SpoolDir,
			SpoolBytes:   cfg.SpoolMaxBytes,
			PluginConfig: pluginCfg,
		},
	})
}

// pluginConfig turns the operator's agent settings into per-plugin
// pluginsdk.ExecRequest.Config maps. It reads agent.tls_ca_file, so a bad
// path fails the agent at start rather than the first ftps transfer.
func pluginConfig(cfg config.Agent) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	if cfg.AllowInsecureHostKeys {
		out["ssh"] = map[string]string{ssh.ConfigAllowInsecureHostKeys: "true"}
		out["transfer"] = map[string]string{transfer.ConfigAllowInsecureHostKeys: "true"}
	}
	if cfg.AllowInsecureHostKeys || cfg.AllowInsecureGit {
		out["ansible"] = map[string]string{}
	}
	if cfg.AllowInsecureHostKeys || cfg.AllowInsecureGit || cfg.AllowInsecureRegistry {
		out["buildah"] = map[string]string{}
	}
	if cfg.AllowInsecureHostKeys {
		out["buildah"][buildah.ConfigAllowInsecureHostKeys] = "true"
	}
	if cfg.AllowInsecureGit {
		out["buildah"][buildah.ConfigAllowInsecureGit] = "true"
	}
	if cfg.AllowInsecureRegistry {
		out["buildah"][buildah.ConfigAllowInsecureRegistry] = "true"
	}
	if cfg.AllowInsecureHostKeys {
		out["ansible"][ansible.ConfigAllowInsecureHostKeys] = "true"
	}
	if cfg.AllowInsecureGit {
		out["ansible"][ansible.ConfigAllowInsecureGit] = "true"
	}
	if cfg.AllowInsecureHostKeys || cfg.AllowInsecureGit || cfg.AllowHelmInCluster {
		out["helm"] = map[string]string{}
	}
	if cfg.AllowInsecureHostKeys {
		out["helm"][helm.ConfigAllowInsecureHostKeys] = "true"
	}
	if cfg.AllowInsecureGit {
		out["helm"][helm.ConfigAllowInsecureGit] = "true"
	}
	if cfg.AllowHelmInCluster {
		out["helm"][helm.ConfigAllowInCluster] = "true"
	}
	if cfg.AllowInsecureWinRM {
		out["winrm"] = map[string]string{winrm.ConfigAllowInsecureWinRM: "true"}
	}
	if cfg.AllowPlainFTP {
		if out["transfer"] == nil {
			out["transfer"] = map[string]string{}
		}
		out["transfer"][transfer.ConfigAllowPlainFTP] = "true"
	}
	if cfg.AllowPlainS3 {
		if out["transfer"] == nil {
			out["transfer"] = map[string]string{}
		}
		out["transfer"][transfer.ConfigAllowPlainS3] = "true"
	}
	if cfg.TLSCAFile != "" {
		pem, err := os.ReadFile(cfg.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("reading agent.tls_ca_file: %w", err)
		}
		if !x509.NewCertPool().AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("agent.tls_ca_file %s holds no PEM certificate", cfg.TLSCAFile)
		}
		if out["transfer"] == nil {
			out["transfer"] = map[string]string{}
		}
		out["transfer"][transfer.ConfigTLSCAPEM] = string(pem)
		if out["winrm"] == nil {
			out["winrm"] = map[string]string{}
		}
		out["winrm"][winrm.ConfigTLSCAPEM] = string(pem)
		if out["ansible"] == nil {
			out["ansible"] = map[string]string{}
		}
		out["ansible"][ansible.ConfigTLSCAPEM] = string(pem)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// runAgent connects to the controller and blocks until ctx is cancelled or
// the controller refuses the agent for good (bad token, pool conflict).
func runAgent(ctx context.Context, cfg config.Config, log *slog.Logger, plugins []pluginsdk.Plugin) error {
	a, err := newAgent(cfg.Agent, cfg.Agent.ControllerURL, cfg.Agent.Token, log, plugins)
	if err != nil {
		return err
	}
	// The signal context must not cancel the agent: shutdown drains first.
	if err := a.Start(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	var fatal error
	select {
	case <-ctx.Done():
		log.Info("stopping")
	case <-a.Done():
		fatal = a.Err()
	}
	if fatal == nil && ctx.Err() != nil {
		dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.DrainGraceDuration())
		a.Drain(dctx)
		dcancel()
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentStopTimeout)
	defer cancel()
	return errors.Join(fatal, a.Stop(sctx))
}

// startDevAgent provisions the pool and a fresh pool token in the dev store
// and starts an agent against the controller listening on addr. The token is
// handed straight to the agent: it is never printed or logged, and tokens
// from earlier runs are revoked so the dev database does not accumulate them.
func startDevAgent(ctx context.Context, st store.Store, cfg config.Config, addr string, log *slog.Logger, plugins []pluginsdk.Plugin) (*agent.Agent, error) {
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
	a, err := newAgent(cfg.Agent, url, plain, log, plugins)
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

// controllerHTTPClient trusts agent.controller_ca_file in addition to the
// system roots for the wss:// connection. Nil (the default client) without it.
func controllerHTTPClient(cfg config.Agent) (*http.Client, error) {
	if cfg.ControllerCAFile == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(cfg.ControllerCAFile)
	if err != nil {
		return nil, fmt.Errorf("reading agent.controller_ca_file: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("agent.controller_ca_file %s holds no PEM certificate", cfg.ControllerCAFile)
	}
	// The default transport's timeouts and proxy handling, with the extra roots.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	return &http.Client{Transport: tr}, nil
}
