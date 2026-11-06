// Package damsoncli is the entry point of the damson command. cmd/damson calls
// it, and so does the main package that "damson build --with" generates for a
// custom binary with extra plugins (ADR-0050).
//
// The package is released with Damson's version. Fields may be added to
// Options and Module but never removed. A plugin must not import it: plugins
// import only pkg/pluginsdk.
package damsoncli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/denyszorinets/damson/internal/cli"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Module is a Go module linked into a custom binary.
type Module struct {
	// Path is the module path, shown by "damson version" and in errors.
	Path string
	// Plugins is the Plugins function of the module's root package.
	Plugins func() []pluginsdk.Plugin
}

// Options configure Main.
type Options struct {
	// Modules are the extra plugin modules, in the order they were requested.
	Modules []Module
}

// Main runs damson with os.Args, handles SIGINT and SIGTERM and exits the
// process with damson's exit code. It never returns.
func Main(opts Options) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	mods := make([]cli.Module, len(opts.Modules))
	for i, m := range opts.Modules {
		mods[i] = cli.Module{Path: m.Path, Plugins: m.Plugins}
	}
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Environ(), cli.Options{Modules: mods})
	stop()
	os.Exit(code)
}
