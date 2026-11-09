// Package cli implements the damson command line. cmd/damson and the custom
// binaries made by "damson build" reach it through pkg/damsoncli.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/denyszorinets/damson/internal/buildcli"
	"github.com/denyszorinets/damson/internal/plugincli"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/all"
)

// Module is an extra module linked into a custom binary.
type Module struct {
	Path    string
	Plugins func() []pluginsdk.Plugin
}

// Options configure Run.
type Options struct{ Modules []Module }

// Run runs damson with args and returns the exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string, opts Options) int {
	plugins, err := linkPlugins(all.Plugins(), opts.Modules)
	if err != nil {
		fmt.Fprintf(stderr, "damson: %v\n", err)
		return exitError
	}
	return dispatch(ctx, args, stdout, stderr, environ, plugins, opts.Modules)
}

// run is Run for the stock binary.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	return Run(ctx, args, stdout, stderr, environ, Options{})
}

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const usage = `Usage: damson <command> [flags]

Commands:
  controller   run the controller role
  agent        run the agent role
  dev          run controller and agent in one process (SQLite)
  admin        manage agent pools and pool tokens
  migrate      apply database migrations (up) or show their state (status)
  apply        apply a folder of job and project files to a controller
  export       write the jobs and projects of a project as files
  plugin       scaffold, check and run plugins (new, validate, run)
  build        build a custom binary with extra plugin modules (--with)
  version      print the version

Run "damson <command> -h" for command flags.
`

func dispatch(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string, plugins []pluginsdk.Plugin, modules []Module) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "controller", "agent", "dev":
		return runRole(ctx, args[0], args[1:], stderr, environ, plugins, modules)
	case "admin":
		return runAdmin(ctx, args[1:], stdout, stderr, environ)
	case "migrate":
		return runMigrate(ctx, args[1:], stdout, stderr, environ)
	case "apply":
		return runApply(ctx, args[1:], stdout, stderr, environ)
	case "export":
		return runExport(ctx, args[1:], stdout, stderr, environ)
	case "plugin":
		return plugincli.Run(ctx, args[1:], plugincli.Env{Stdin: os.Stdin, Stdout: stdout, Stderr: stderr, Plugins: plugins})
	case "build":
		return buildcli.Run(ctx, args[1:], buildcli.Env{Stdout: stdout, Stderr: stderr, Environ: environ})
	case "version":
		return runVersion(stdout, modules)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "damson: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}
