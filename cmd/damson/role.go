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
// cancelled. The real controller and agent land in later issues.
func runRole(ctx context.Context, name string, args []string, stderr io.Writer, environ []string) int {
	fs := flag.NewFlagSet("damson "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "path to a TOML config file (or set "+config.EnvConfigFile+")")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}

	cfg, err := config.Load(config.Role(name), config.FilePath(*cfgPath, environ), environ)
	if err != nil {
		fmt.Fprintf(stderr, "damson %s: %v\n", name, err)
		return exitError
	}

	log := logging.New(stderr, cfg.SlogLevel(), name)
	log.Info("starting", "version", version.Version, "config", slog.AnyValue(cfg.Redacted()))
	<-ctx.Done()
	log.Info("stopping")
	return exitOK
}
