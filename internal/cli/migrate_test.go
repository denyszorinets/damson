package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/store/migrations"
)

func migrateCmd(t *testing.T, env []string, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = run(context.Background(), append([]string{"migrate"}, args...), &o, &e, env)
	return code, o.String(), e.String()
}

func TestMigrateUpAndStatus(t *testing.T) {
	forEachBackend(t, func(t *testing.T, be adminBackend) {
		env := adminEnv(t, be, false)
		dialect := migrations.Postgres
		if be.name == "sqlite" {
			dialect = migrations.SQLite
		}
		latest, err := migrations.Latest(dialect)
		if err != nil || latest == 0 {
			t.Fatalf("Latest = %d, %v", latest, err)
		}

		code, out, _ := migrateCmd(t, env, "status")
		if code != exitOK || !strings.Contains(out, "pending") {
			t.Fatalf("status of a fresh database: %d %q", code, out)
		}
		if code, _, _ := migrateCmd(t, env, "status", "--check"); code != exitError {
			t.Errorf("--check on pending migrations exited %d, want %d", code, exitError)
		}
		// status never migrates.
		if code, out, _ := migrateCmd(t, env, "status", "--json"); code != exitOK {
			t.Fatal("status --json failed")
		} else {
			var s migrateState
			if err := json.Unmarshal([]byte(out), &s); err != nil || s.Database != 0 || s.Binary != latest || s.Pending != latest {
				t.Fatalf("status = %+v, %v", s, err)
			}
		}

		code, out, errOut := migrateCmd(t, env, "up")
		if code != exitOK || !strings.Contains(out, "migrated from version 0") {
			t.Fatalf("up: %d %q %q", code, out, errOut)
		}
		code, out, _ = migrateCmd(t, env, "status", "--check")
		if code != exitOK || !strings.Contains(out, "up to date") {
			t.Fatalf("status after up: %d %q", code, out)
		}
		// Running it again changes nothing.
		if code, out, errOut := migrateCmd(t, env, "up"); code != exitOK || !strings.Contains(out, "from version") {
			t.Fatalf("second up: %d %q %q", code, out, errOut)
		}
		st := openStore(t, be, env)
		if v, err := st.MigrationVersion(context.Background()); err != nil || v != latest {
			t.Fatalf("version = %d, %v; want %d", v, err, latest)
		}
	})
}

func TestMigrateUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"down"}, {"up", "extra"}, {"up", "--check"}} {
		if code, _, errOut := migrateCmd(t, nil, args...); code != exitUsage || !strings.Contains(errOut, "Usage: damson migrate") {
			t.Errorf("%v: exit %d, stderr %q", args, code, errOut)
		}
	}
}
