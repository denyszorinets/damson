package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/migrations"
	"github.com/denyszorinets/damson/internal/store/storefactory"
)

const migrateUsage = `Usage: damson migrate <up|status> [--config FILE] [--json] [--check]

  up       apply the pending database migrations (and the job queue's own)
           and exit; safe to run while controllers start (Postgres: advisory lock)
  status   show the database's migration version, the binary's, and what is pending;
           with --check, exit 1 when migrations are pending or the database is
           newer than this binary

The database settings come from the controller configuration. Migrations only
move forward: there is no down; to go back, restore a backup (docs: Upgrade).
`

// migrateState is what `migrate status` and `migrate up` report.
type migrateState struct {
	Database int64 `json:"database_version"`
	Binary   int64 `json:"binary_version"`
	Pending  int64 `json:"pending"`
	Newer    bool  `json:"database_newer_than_binary"`
}

func runMigrate(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	fs := flag.NewFlagSet("damson migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "path to a TOML config file (or set "+config.EnvConfigFile+")")
	asJSON := fs.Bool("json", false, "print JSON")
	check := fs.Bool("check", false, "status: exit 1 when migrations are pending or the database is newer than this binary")
	if len(args) == 0 {
		fmt.Fprint(stderr, migrateUsage)
		return exitUsage
	}
	action := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if (action != "up" && action != "status") || fs.NArg() != 0 || (*check && action != "status") {
		fmt.Fprint(stderr, migrateUsage)
		return exitUsage
	}
	cfg, err := config.Load(config.RoleController, config.FilePath(*cfgPath, environ), environ)
	if err != nil {
		fmt.Fprintf(stderr, "damson migrate: %v\n", err)
		return exitError
	}
	st, err := storefactory.Open(ctx, cfg.Database)
	if err != nil {
		fmt.Fprintf(stderr, "damson migrate: opening store: %v\n", err)
		return exitError
	}
	defer func() { _ = st.Close() }()
	dialect := migrations.Postgres
	if cfg.Database.Driver == config.DriverSQLite {
		dialect = migrations.SQLite
	}
	before, err := migrateStatus(ctx, st, dialect)
	if err != nil {
		fmt.Fprintf(stderr, "damson migrate: %v\n", err)
		return exitError
	}
	if action == "up" {
		if before.Newer {
			fmt.Fprintf(stderr, "damson migrate: the database is at version %d, newer than this binary (%d); run a newer damson\n", before.Database, before.Binary)
			return exitError
		}
		if err := st.Migrate(ctx); err != nil {
			fmt.Fprintf(stderr, "damson migrate: %v\n", err)
			return exitError
		}
		after, err := migrateStatus(ctx, st, dialect)
		if err != nil {
			fmt.Fprintf(stderr, "damson migrate: %v\n", err)
			return exitError
		}
		return printMigrate(stdout, stderr, *asJSON, fmt.Sprintf("migrated from version %d to %d", before.Database, after.Database), after)
	}
	code := printMigrate(stdout, stderr, *asJSON, describeMigrate(before), before)
	if code == exitOK && *check && (before.Pending > 0 || before.Newer) {
		return exitError
	}
	return code
}

func migrateStatus(ctx context.Context, st store.Store, d migrations.Dialect) (migrateState, error) {
	db, err := st.MigrationVersion(ctx)
	if err != nil {
		return migrateState{}, fmt.Errorf("reading the migration version: %w", err)
	}
	bin, err := migrations.Latest(d)
	if err != nil {
		return migrateState{}, err
	}
	s := migrateState{Database: db, Binary: bin, Newer: db > bin}
	if bin > db {
		s.Pending = bin - db
	}
	return s, nil
}

func describeMigrate(s migrateState) string {
	switch {
	case s.Newer:
		return fmt.Sprintf("database version %d is newer than this binary (%d): run a newer damson", s.Database, s.Binary)
	case s.Pending > 0:
		return fmt.Sprintf("database version %d, binary %d: %d pending (run \"damson migrate up\")", s.Database, s.Binary, s.Pending)
	}
	return fmt.Sprintf("database version %d, up to date", s.Database)
}

func printMigrate(stdout, stderr io.Writer, asJSON bool, msg string, s migrateState) int {
	if asJSON {
		if err := writeJSON(stdout, s); err != nil {
			fmt.Fprintf(stderr, "damson migrate: %v\n", err)
			return exitError
		}
		return exitOK
	}
	fmt.Fprintln(stdout, msg)
	return exitOK
}
