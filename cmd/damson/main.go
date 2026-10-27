// Command damson is the single Damson binary: controller, agent or dev mode.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Environ()))
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
  version      print the version

Run "damson <command> -h" for command flags.
`

func run(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "controller", "agent", "dev":
		return runRole(ctx, args[0], args[1:], stderr, environ)
	case "admin":
		return runAdmin(ctx, args[1:], stdout, stderr, environ)
	case "version":
		return runVersion(stdout)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "damson: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}
