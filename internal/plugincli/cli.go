// Package plugincli implements "damson plugin": scaffolding, checking and
// running plugins for plugin authors (ARCHITECTURE.md section 6, developer
// tooling). It works on the plugins linked into the running binary, because v1
// plugins are compiled in; an out-of-tree plugin is linked in with
// "damson build --with" first.
package plugincli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Exit codes.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// Env is what the commands need from the process.
type Env struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	// Plugins are the plugins linked into the binary.
	Plugins []pluginsdk.Plugin
}

const usage = `Usage: damson plugin <command> [flags]

Commands:
  new <name>        scaffold an out-of-tree plugin module
                      --dir <dir>        target directory (default ./damson-plugin-<name>)
                      --module <path>    Go module path (default example.com/damson-plugin-<name>)
  validate [ref]    check the manifests and schemas of the linked plugins
                    (all of them, or one: name or name@major)
  run <ref>         run one linked plugin locally, without a controller
                      --input <file>     JSON input ("-" is stdin; default {})
                      --secrets <file>   JSON secrets (mode 0600 or stricter)
                      --nodes <file>     JSON nodes, keyed by name
                      --config k=v       plugin setting (repeatable)
                      --timeout <dur>    stop the plugin after this long

Plugins are compiled in: a plugin you wrote is linked into a custom binary with
"damson build --with <module>@<version>", which then also has these commands.
`

// Run executes "damson plugin <args>".
func Run(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 {
		fmt.Fprint(env.Stderr, usage)
		return ExitUsage
	}
	switch args[0] {
	case "new":
		return runNew(args[1:], env)
	case "validate":
		return runValidate(args[1:], env)
	case "run":
		return runRun(ctx, args[1:], env)
	case "-h", "--help", "help":
		fmt.Fprint(env.Stdout, usage)
		return ExitOK
	}
	fmt.Fprintf(env.Stderr, "damson plugin: unknown command %q\n\n%s", args[0], usage)
	return ExitUsage
}

// newFlags makes a flag set that reports errors to stderr and not to exit.
func newFlags(name string, env Env) *flag.FlagSet {
	fs := flag.NewFlagSet("damson plugin "+name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	return fs
}

// parse parses args (flags may follow the positional argument) and returns the
// positionals.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		before := args
		args = fs.Args()
		// After a "--" everything that is left is positional.
		if len(args) < len(before) && before[len(before)-len(args)-1] == "--" {
			return append(pos, args...), nil
		}
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	return pos, nil
}
