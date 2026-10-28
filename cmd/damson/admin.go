package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/storefactory"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

const adminUsage = `Usage: damson admin <resource> <action> [flags]

Resources and actions:
  pool create <name> [--description d]     create an agent pool
  pool list                                list agent pools
  token create --pool <name> --name <n>    create a pool token (printed once)
  token list --pool <name>                 list a pool's tokens
  token revoke <token-id>                  revoke a pool token

All actions accept --config <path> and --json.
`

// adminCmd is one parsed admin action.
type adminCmd struct {
	stdout io.Writer
	stderr io.Writer
	json   bool
	args   []string // positional arguments
	pool   string
	name   string
	desc   string
}

type adminAction struct {
	// flags lists the flag names the action accepts beyond --config/--json.
	flags []string
	// required lists flags that must be non-empty.
	required []string
	nargs    int
	run      func(ctx context.Context, st store.Store, c adminCmd, now time.Time) error
}

var adminActions = map[string]adminAction{
	"pool create":  {flags: []string{"description"}, nargs: 1, run: poolCreate},
	"pool list":    {run: poolList},
	"token create": {flags: []string{"pool", "name"}, required: []string{"pool", "name"}, run: tokenCreate},
	"token list":   {flags: []string{"pool"}, required: []string{"pool"}, run: tokenList},
	"token revoke": {nargs: 1, run: tokenRevoke},
}

// runAdmin dispatches `damson admin <resource> <action>`. Only `token create`
// prints a token (to stdout); nothing here logs one.
func runAdmin(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprint(stdout, adminUsage)
		return exitOK
	}
	if len(args) < 2 {
		fmt.Fprint(stderr, adminUsage)
		return exitUsage
	}
	key := args[0] + " " + args[1]
	act, ok := adminActions[key]
	if !ok {
		fmt.Fprintf(stderr, "damson admin: unknown command %q\n\n%s", key, adminUsage)
		return exitUsage
	}
	fs := flag.NewFlagSet("damson admin "+key, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "path to a TOML config file (or set "+config.EnvConfigFile+")")
	c := adminCmd{stdout: stdout, stderr: stderr}
	fs.BoolVar(&c.json, "json", false, "machine-readable JSON output")
	for _, f := range act.flags {
		switch f {
		case "description":
			fs.StringVar(&c.desc, f, "", "pool description")
		case "pool":
			fs.StringVar(&c.pool, f, "", "pool name")
		case "name":
			fs.StringVar(&c.name, f, "", "token name")
		}
	}
	pos, err := parseInterspersed(fs, args[2:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	c.args = pos
	if err := checkUsage(act, c); err != nil {
		fmt.Fprintf(stderr, "damson admin %s: %v\n", key, err)
		return exitUsage
	}
	if err := execAdmin(ctx, act, c, *cfgPath, environ); err != nil {
		fmt.Fprintf(stderr, "damson admin %s: %v\n", key, err)
		return exitError
	}
	return exitOK
}

// parseInterspersed parses flags that may appear before or after positionals.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func checkUsage(act adminAction, c adminCmd) error {
	if len(c.args) != act.nargs {
		return fmt.Errorf("expected %d argument(s), got %d", act.nargs, len(c.args))
	}
	for _, a := range c.args {
		if strings.TrimSpace(a) == "" {
			return errors.New("empty argument")
		}
	}
	vals := map[string]string{"pool": c.pool, "name": c.name, "description": c.desc}
	for _, f := range act.required {
		if strings.TrimSpace(vals[f]) == "" {
			return fmt.Errorf("--%s is required", f)
		}
	}
	return nil
}

func execAdmin(ctx context.Context, act adminAction, c adminCmd, cfgPath string, environ []string) error {
	cfg, err := config.Load(config.RoleController, config.FilePath(cfgPath, environ), environ)
	if err != nil {
		return err
	}
	st, err := storefactory.Open(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer func() { _ = st.Close() }()
	v, err := st.MigrationVersion(ctx)
	if err != nil {
		return fmt.Errorf("reading migration version: %w", err)
	}
	if v == 0 {
		return errors.New("database is not migrated; run \"damson controller --auto-migrate\" (or \"damson dev\") first")
	}
	return act.run(ctx, st, c, time.Now().UTC())
}

func poolCreate(ctx context.Context, st store.Store, c adminCmd, now time.Time) error {
	p, err := st.CreatePool(ctx, c.args[0], c.desc, now)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return fmt.Errorf("pool %q already exists", c.args[0])
		}
		return fmt.Errorf("creating pool: %w", err)
	}
	return printPools(c, []store.Pool{p})
}

func poolList(ctx context.Context, st store.Store, c adminCmd, _ time.Time) error {
	ps, err := st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}
	return printPools(c, ps)
}

type poolJSON struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

func printPools(c adminCmd, ps []store.Pool) error {
	if c.json {
		out := make([]poolJSON, 0, len(ps))
		for _, p := range ps {
			out = append(out, poolJSON{p.ID, p.Name, p.Description, p.CreatedAt})
		}
		return writeJSON(c.stdout, out)
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tDESCRIPTION\tCREATED")
	for _, p := range ps {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.ID, p.Name, p.Description, fmtTime(&p.CreatedAt))
	}
	return tw.Flush()
}

func lookupPool(ctx context.Context, st store.Store, name string) (store.Pool, error) {
	p, err := st.GetPoolByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return p, fmt.Errorf("pool %q not found", name)
	}
	if err != nil {
		return p, fmt.Errorf("looking up pool: %w", err)
	}
	return p, nil
}

func tokenCreate(ctx context.Context, st store.Store, c adminCmd, now time.Time) error {
	p, err := lookupPool(ctx, st, c.pool)
	if err != nil {
		return err
	}
	plain, hash, err := tokens.New()
	if err != nil {
		return err
	}
	t, err := st.CreatePoolToken(ctx, p.ID, c.name, hash, now)
	if err != nil {
		return fmt.Errorf("creating token: %w", err)
	}
	if c.json {
		err = writeJSON(c.stdout, struct {
			tokenJSON
			Token string `json:"token"`
		}{toTokenJSON(t), plain})
	} else {
		_, err = fmt.Fprintln(c.stdout, plain)
	}
	if err != nil {
		return fmt.Errorf("writing token: %w", err)
	}
	// The hint goes to stderr so stdout stays the bare token for scripts.
	fmt.Fprintln(c.stderr, "Store this token now: it is shown only once and cannot be retrieved later.")
	return nil
}

func tokenList(ctx context.Context, st store.Store, c adminCmd, _ time.Time) error {
	p, err := lookupPool(ctx, st, c.pool)
	if err != nil {
		return err
	}
	ts, err := st.ListPoolTokens(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("listing tokens: %w", err)
	}
	if c.json {
		out := make([]tokenJSON, 0, len(ts))
		for _, t := range ts {
			out = append(out, toTokenJSON(t))
		}
		return writeJSON(c.stdout, out)
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tCREATED\tLAST USED\tREVOKED")
	for _, t := range ts {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Name, fmtTime(&t.CreatedAt), fmtTime(t.LastUsedAt), fmtTime(t.RevokedAt))
	}
	return tw.Flush()
}

func tokenRevoke(ctx context.Context, st store.Store, c adminCmd, now time.Time) error {
	id := c.args[0]
	switch err := st.RevokePoolToken(ctx, id, now); {
	case errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("token %q not found", id)
	case errors.Is(err, store.ErrStale):
		return fmt.Errorf("token %q is already revoked", id)
	case err != nil:
		return fmt.Errorf("revoking token: %w", err)
	}
	if c.json {
		return writeJSON(c.stdout, map[string]string{"id": id, "status": "revoked"})
	}
	fmt.Fprintf(c.stdout, "revoked token %s\n", id)
	return nil
}

type tokenJSON struct {
	ID         string     `json:"id"`
	PoolID     string     `json:"pool_id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

func toTokenJSON(t store.PoolToken) tokenJSON {
	return tokenJSON{t.ID, t.PoolID, t.Name, t.CreatedAt, t.LastUsedAt, t.RevokedAt}
}

func fmtTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
