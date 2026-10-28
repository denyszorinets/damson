package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

func admin(t *testing.T, env []string, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = run(context.Background(), append([]string{"admin"}, args...), &o, &e, env)
	return code, o.String(), e.String()
}

func mustAdmin(t *testing.T, env []string, args ...string) string {
	t.Helper()
	code, out, errOut := admin(t, env, args...)
	if code != exitOK {
		t.Fatalf("%v: exit %d, stderr: %s", args, code, errOut)
	}
	return out
}

func TestAdminUsageAndErrors(t *testing.T) {
	forEachBackend(t, testAdminUsageAndErrors)
}

func testAdminUsageAndErrors(t *testing.T, be adminBackend) {
	migrated := adminEnv(t, be, true)
	tests := []struct {
		name     string
		env      []string
		args     []string
		wantCode int
		wantErr  string
	}{
		{"no args", nil, nil, exitUsage, "Usage:"},
		{"one arg", nil, []string{"pool"}, exitUsage, "Usage:"},
		{"unknown", nil, []string{"pool", "nuke"}, exitUsage, "unknown command"},
		{"bad flag", nil, []string{"pool", "list", "-bogus"}, exitUsage, ""},
		{"pool create no name", nil, []string{"pool", "create"}, exitUsage, "expected 1 argument"},
		{"pool list extra arg", nil, []string{"pool", "list", "x"}, exitUsage, "expected 0 argument"},
		{"token create no pool", nil, []string{"token", "create", "--name", "n"}, exitUsage, "--pool is required"},
		{"token create no name", nil, []string{"token", "create", "--pool", "p"}, exitUsage, "--name is required"},
		{"token list no pool", nil, []string{"token", "list"}, exitUsage, "--pool is required"},
		{"token revoke no id", nil, []string{"token", "revoke"}, exitUsage, "expected 1 argument"},
		{"not migrated", adminEnv(t, be, false), []string{"pool", "list"}, exitError, "not migrated"},
		{"token unknown pool", migrated, []string{"token", "list", "--pool", "nope"}, exitError, `pool "nope" not found`},
		{"revoke unknown token", migrated, []string{"token", "revoke", "nope"}, exitError, "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, errOut := admin(t, tt.env, tt.args...)
			if code != tt.wantCode {
				t.Fatalf("exit = %d, want %d; stderr: %s", code, tt.wantCode, errOut)
			}
			if !strings.Contains(errOut, tt.wantErr) {
				t.Errorf("stderr %q missing %q", errOut, tt.wantErr)
			}
		})
	}
}

func TestAdminPools(t *testing.T) {
	forEachBackend(t, testAdminPools)
}

func testAdminPools(t *testing.T, be adminBackend) {
	env := adminEnv(t, be, true)
	out := mustAdmin(t, env, "pool", "create", "prod", "--description", "production")
	if !strings.Contains(out, "prod") || !strings.Contains(out, "production") {
		t.Errorf("create output: %q", out)
	}
	// flag after the positional also works
	mustAdmin(t, env, "pool", "create", "--description", "d2", "staging")

	code, _, errOut := admin(t, env, "pool", "create", "prod")
	if code != exitError || !strings.Contains(errOut, "already exists") {
		t.Fatalf("duplicate: exit %d, stderr %q", code, errOut)
	}

	table := mustAdmin(t, env, "pool", "list")
	for _, want := range []string{"NAME", "prod", "staging", "d2"} {
		if !strings.Contains(table, want) {
			t.Errorf("list missing %q: %q", want, table)
		}
	}
	var pools []poolJSON
	if err := json.Unmarshal([]byte(mustAdmin(t, env, "pool", "list", "--json")), &pools); err != nil {
		t.Fatal(err)
	}
	if len(pools) != 2 {
		t.Errorf("json pools = %d, want 2", len(pools))
	}
}

func TestAdminTokenLifecycle(t *testing.T) {
	forEachBackend(t, testAdminTokenLifecycle)
}

func testAdminTokenLifecycle(t *testing.T, be adminBackend) {
	env := adminEnv(t, be, true)
	mustAdmin(t, env, "pool", "create", "prod")

	var o, e bytes.Buffer
	code := run(context.Background(), []string{"admin", "token", "create", "--pool", "prod", "--name", "ci"}, &o, &e, env)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, e.String())
	}
	token := strings.TrimSpace(o.String())
	if !strings.HasPrefix(token, tokens.Prefix) || strings.Contains(token, "\n") {
		t.Fatalf("stdout should be the bare token, got %q", o.String())
	}
	if strings.Contains(e.String(), token) {
		t.Error("token leaked into stderr")
	}
	if !strings.Contains(e.String(), "only once") {
		t.Errorf("stderr missing hint: %q", e.String())
	}

	st := openStore(t, be, env)
	ctx := context.Background()
	if _, _, err := st.LookupPoolToken(ctx, token); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("plaintext must not be stored as the hash, err = %v", err)
	}
	pool, tok, err := st.LookupPoolToken(ctx, tokens.Hash(token))
	if err != nil {
		t.Fatalf("lookup by hash: %v", err)
	}
	if pool.Name != "prod" || tok.Name != "ci" {
		t.Errorf("lookup = %q/%q", pool.Name, tok.Name)
	}

	list := mustAdmin(t, env, "token", "list", "--pool", "prod")
	if !strings.Contains(list, "ci") || strings.Contains(list, tokens.Hash(token)) || strings.Contains(list, token) {
		t.Errorf("list output: %q", list)
	}

	mustAdmin(t, env, "token", "revoke", tok.ID)
	if _, _, err := st.LookupPoolToken(ctx, tokens.Hash(token)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("revoked lookup err = %v, want ErrNotFound", err)
	}
	if code, _, errOut := admin(t, env, "token", "revoke", tok.ID); code != exitError || !strings.Contains(errOut, "already revoked") {
		t.Errorf("second revoke: exit %d, stderr %q", code, errOut)
	}

	var listed []tokenJSON
	if err := json.Unmarshal([]byte(mustAdmin(t, env, "token", "list", "--pool", "prod", "--json")), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].RevokedAt == nil {
		t.Errorf("revoked token should be listed with revoked_at: %+v", listed)
	}
}

func TestAdminTokenCreateJSON(t *testing.T) {
	forEachBackend(t, testAdminTokenCreateJSON)
}

func testAdminTokenCreateJSON(t *testing.T, be adminBackend) {
	env := adminEnv(t, be, true)
	mustAdmin(t, env, "pool", "create", "prod")
	var o, e bytes.Buffer
	if code := run(context.Background(), []string{"admin", "token", "create", "--pool", "prod", "--name", "ci", "--json"}, &o, &e, env); code != exitOK {
		t.Fatalf("exit %d: %s", code, e.String())
	}
	var got struct {
		Token string `json:"token"`
		ID    string `json:"id"`
	}
	if err := json.Unmarshal(o.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.Token, tokens.Prefix) || got.ID == "" {
		t.Errorf("json = %s", o.String())
	}
	if strings.Contains(e.String(), got.Token) {
		t.Error("token leaked into stderr")
	}
}
