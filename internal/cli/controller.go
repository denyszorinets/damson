package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/denyszorinets/damson/internal/agent"
	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/controller/node"
	"github.com/denyszorinets/damson/internal/pluginreg"
	secretsdb "github.com/denyszorinets/damson/internal/secrets/db"
	"github.com/denyszorinets/damson/internal/store/storefactory"
	"github.com/denyszorinets/damson/internal/webui"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/all"
	"github.com/denyszorinets/damson/web"
)

// shutdownTimeout bounds how long in-flight requests may drain after a signal.
const shutdownTimeout = 15 * time.Second

// runController opens the store, optionally migrates it, serves the HTTP API
// on the configured address and blocks until ctx is cancelled. It then shuts
// down in reverse order: in-process agent (dev only), HTTP server, gateway,
// then the store. With dev set it also starts an agent in this process,
// connected to the gateway through a freshly provisioned pool token.
func runController(ctx context.Context, cfg config.Config, log *slog.Logger, modules []Module, linked []pluginsdk.Plugin, migrate, dev bool, devNodesPath string) error {
	if !dev && len(modules) > 0 {
		log.Warn("this binary links extra plugin modules; the controller does not use them but they are fully trusted code in this process (ADR-0050): run the stock binary for controllers",
			"modules", modulePaths(modules))
	}
	st, err := storefactory.Open(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Warn("closing store", "error", err)
		}
	}()
	if migrate {
		if err := st.Migrate(ctx); err != nil {
			return fmt.Errorf("migrating database: %w", err)
		}
		log.Info("migrations applied")
	}

	devKEKFile := ""
	if dev {
		if devKEKFile, err = ensureDevKEK(ctx, st, &cfg, log); err != nil {
			return err
		}
	}
	devNodes, err := loadDevNodes(devNodesPath, log)
	if err != nil {
		return err
	}
	if !dev && !cfg.Auth.RequireToken && !cfg.OIDCEnabled() {
		log.Warn("auth.require_token is off: the job, run and execution API accepts requests without a token (interim until OIDC; ADR-0060)")
	}
	oidcAPI, err := newOIDC(cfg, log)
	if err != nil {
		return err
	}
	id, err := controllerID()
	if err != nil {
		return err
	}
	plugins, err := newPluginRegistry(log)
	if err != nil {
		return err
	}
	secret, previous, err := cfg.ClusterSecrets()
	if err != nil {
		return err
	}
	if dev { // one process: nothing to proxy to
		secret, previous = "", ""
	}
	blobs, err := buildLogBlobs(ctx, cfg, st, log)
	if err != nil {
		return err
	}
	ob, err := openBaoConfig(cfg)
	if err != nil {
		return err
	}
	n, err := node.Start(ctx, node.Config{
		LogBlobs: blobs, OpenBao: ob,
		SingleController: cfg.Database.Driver == config.DriverSQLite,
		ClusterSecret:    secret, ClusterSecretPrevious: previous, InternalListen: cfg.Controller.InternalListen,
		Store: st, ControllerID: id, InternalURL: cfg.Controller.InternalURL, Plugins: plugins, DevFile: devNodes, KEK: cfg.Secrets.KEK, KEKPrevious: cfg.Secrets.KEKPrevious, FinallyGrace: cfg.FinallyGraceDuration(),
		RequireToken: cfg.Auth.RequireToken, OIDC: oidcAPI, InsecureDev: cfg.Controller.InsecureDev, TrustForwardedProto: cfg.Controller.TrustForwardedProto,
		Addr: cfg.Controller.Listen, UI: webui.Handler(web.Build()), Logger: log,
	})
	if err != nil {
		if devKEKFile != "" && errors.Is(err, secretsdb.ErrKEK) {
			return fmt.Errorf("%w\n(the dev key file is %s; it belongs to another database or was replaced: restore the matching one, or delete the database files %s, %s-wal, %s-shm and the key file to start over)", err, devKEKFile, strings.TrimSuffix(devKEKFile, ".kek"), strings.TrimSuffix(devKEKFile, ".kek"), strings.TrimSuffix(devKEKFile, ".kek"))
		}
		return err
	}
	stopRetention, err := startRetention(ctx, st, cfg, id, log)
	if err != nil {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		return errors.Join(err, n.Stop(sctx))
	}
	defer stopRetention() // runs on every exit path
	srv := n.Server()
	if err := n.Listen(ctx); err != nil {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		return errors.Join(err, n.Stop(sctx))
	}
	var devAgent *agent.Agent
	if dev {
		if devAgent, err = startDevAgent(ctx, st, cfg, srv.Addr(), log, linked); err != nil {
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
			defer cancel()
			if ctx.Err() != nil && errors.Is(err, context.Canceled) {
				err = nil // shut down while provisioning: a clean stop
			}
			return errors.Join(err, n.Stop(sctx))
		}
	}
	serveErr := awaitShutdown(ctx, log, srv.Err(), devAgent)

	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	// Stop the dev agent and the listener first so no new agents arrive, then
	// tell the connected ones to reconnect elsewhere and deregister
	// (ARCHITECTURE 8.5).
	return errors.Join(serveErr, stopDevAgent(sctx, devAgent, log), n.Stop(sctx))
}

// controllerID is the hostname plus a boot nonce, unique per process.
func controllerID() (string, error) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "controller"
	}
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generating controller id: %w", err)
	}
	return host + "-" + hex.EncodeToString(nonce[:]), nil
}

// awaitShutdown blocks until ctx is cancelled, the HTTP server fails or the
// in-process dev agent (nil outside dev) ends fatally, and returns the error
// that ended it, if any.
func awaitShutdown(ctx context.Context, log *slog.Logger, srvErr <-chan error, devAgent *agent.Agent) error {
	var agentDone <-chan struct{}
	if devAgent != nil {
		agentDone = devAgent.Done()
	}
	select {
	case <-ctx.Done():
		log.Info("stopping")
		return nil
	case err := <-srvErr:
		log.Error("http server failed, stopping", "error", err)
		return err
	case <-agentDone:
		if devAgent.Err() == nil {
			// The agent exits cleanly when ctx is cancelled; that is a normal shutdown.
			log.Info("stopping")
			return nil
		}
		err := fmt.Errorf("dev agent stopped: %w", devAgent.Err())
		log.Error("dev agent ended, stopping", "error", err)
		return err
	}
}

// newPluginRegistry links the built-in plugins into a registry, so their
// schemas are known before any agent connects (ARCHITECTURE section 6).
func newPluginRegistry(log *slog.Logger) (*pluginreg.Registry, error) {
	var ms []pluginsdk.Manifest
	for _, p := range all.Plugins() {
		ms = append(ms, p.Manifest())
	}
	reg, err := pluginreg.New(ms, log)
	if err != nil {
		return nil, fmt.Errorf("building plugin registry: %w", err)
	}
	return reg, nil
}
