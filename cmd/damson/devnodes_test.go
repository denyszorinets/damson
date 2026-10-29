package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
