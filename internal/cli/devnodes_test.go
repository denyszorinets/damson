package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/store/storefactory"
)

func TestDevNodesGating(t *testing.T) {
	good := filepath.Join(t.TempDir(), "nodes.json")
	if err := os.WriteFile(good, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"version":1,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
		env  []string
		want int
		msg  string
	}{
		{"agent rejects it", []string{"agent", "--dev-nodes", good}, nil, exitUsage, "controller option"},
		{"controller needs insecure-dev", []string{"controller", "--dev-nodes", good}, nil, exitUsage, "--insecure-dev"},
		{"bad file fails startup", []string{"dev", "--dev-nodes", bad}, adminEnvSQLite(t), exitError, "--dev-nodes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var errb syncBuf
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if got := run(ctx, tc.args, io.Discard, &errb, tc.env); got != tc.want || !strings.Contains(errb.String(), tc.msg) {
				t.Fatalf("exit = %d (want %d); stderr: %s", got, tc.want, errb.String())
			}
		})
	}
}

func adminEnvSQLite(t *testing.T) []string {
	t.Helper()
	return append(adminEnv(t, adminBackends()[0], false), "DAMSON_CONTROLLER__LISTEN=127.0.0.1:0")
}

// `damson dev --dev-nodes` seeds the file into the database: the node as source
// devfile, the secrets encrypted with the generated dev key; a restart changes
// nothing.
func TestDevNodesSeedTheDatabase(t *testing.T) {
	env := ctrlEnv(t)
	dsn := strings.TrimPrefix(env[0], "DAMSON_DATABASE__DSN=")
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	if err := os.WriteFile(keyFile, []byte("KEY-MARKER-BODY"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "nodes.json")
	body := `{"version":1,"nodes":{"dev-1":{"address":"10.0.0.1","host_key_policy":"strict","credential":"secret://_inventory/k"}},
"secrets":{"secret://_inventory/k":{"username":"u","private_key_file":"` + keyFile + `"}}}`
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		code, log := startAndStop(t, env, "dev", "--dev-nodes", file)
		if code != exitOK || strings.Contains(log, "KEY-MARKER-BODY") {
			t.Fatalf("start %d: exit %d; %s", i, code, log)
		}
	}
	if _, err := os.Stat(dsn + ".kek"); err != nil {
		t.Fatalf("no dev key file: %v", err)
	}
	st, err := storefactory.Open(context.Background(), config.Database{Driver: config.DriverSQLite, DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	n, err := st.GetNode(context.Background(), "dev-1")
	if err != nil || n.Source != "devfile" || n.Version != 1 {
		t.Fatalf("node = %+v, %v; want source devfile at version 1 after two starts", n, err)
	}
	sm, blob, err := st.GetSecret(context.Background(), "_inventory", "k")
	if err != nil || sm.CurrentVersion != 1 || strings.Contains(string(blob.Ciphertext), "KEY-MARKER-BODY") {
		t.Fatalf("secret = %+v, %v", sm, err)
	}
}
