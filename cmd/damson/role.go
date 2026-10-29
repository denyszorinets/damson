package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/logging"
	"github.com/denyszorinets/damson/internal/version"
)

func runVersion(stdout io.Writer) int {
	fmt.Fprintln(stdout, "damson", version.Version)
	return exitOK
}

// runRole loads config for the role, logs startup, and blocks until ctx is
// cancelled. Controller and dev serve the HTTP API (dev also runs an agent in
// the same process); the agent connects to the controller.
func runRole(ctx context.Context, name string, args []string, stderr io.Writer, environ []string) int {
	fs := flag.NewFlagSet("damson "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "path to a TOML config file (or set "+config.EnvConfigFile+")")
	autoMigrate := fs.Bool("auto-migrate", false, "apply pending database migrations at start (always on for dev; ignored by agent)")
	insecureDev := fs.Bool("insecure-dev", false, "allow plain ws:// agent connections (development only; or set controller.insecure_dev)")
	devNodes := fs.String("dev-nodes", "", "JSON file with dev-only nodes and secret files (dev, or controller with insecure-dev; ADR-0034)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}

	if *devNodes != "" && name == string(config.RoleAgent) {
		fmt.Fprintln(stderr, "damson agent: --dev-nodes is a controller option")
		return exitUsage
	}

	cfg, err := config.Load(config.Role(name), config.FilePath(*cfgPath, environ), environ)
	if err != nil {
		fmt.Fprintf(stderr, "damson %s: %v\n", name, err)
		return exitError
	}

	log := logging.New(stderr, cfg.SlogLevel(), name)
	log.Info("starting", "version", version.Version, "config", slog.AnyValue(cfg.Redacted()))
	if name == string(config.RoleAgent) {
		if err := runAgent(ctx, cfg, log); err != nil {
			fmt.Fprintf(stderr, "damson agent: %v\n", err)
			return exitError
		}
		log.Info("stopped")
		return exitOK
	}
	if *insecureDev || name == string(config.RoleDev) {
		cfg.Controller.InsecureDev = true // dev serves plain ws:// on loopback
	}
	if *devNodes != "" && !cfg.Controller.InsecureDev {
		fmt.Fprintf(stderr, "damson %s: --dev-nodes needs --insecure-dev (development only)\n", name)
		return exitUsage
	}
	migrate := *autoMigrate || name == string(config.RoleDev)
	if err := runController(ctx, cfg, log, migrate, name == string(config.RoleDev), *devNodes); err != nil {
		fmt.Fprintf(stderr, "damson %s: %v\n", name, err)
		return exitError
	}
	log.Info("stopped")
	return exitOK
}
