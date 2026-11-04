package main

import (
	"context"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/secrets"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

func TestAdminKEKGenerate(t *testing.T) {
	code, out, errOut := admin(t, nil, "kek", "generate")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	key := strings.TrimSpace(out)
	if _, err := secrets.ParseKEK(key); err != nil {
		t.Fatalf("stdout is not a valid KEK: %v", err)
	}
	if strings.Contains(errOut, key) || !strings.Contains(errOut, "kek_id") {
		t.Errorf("stderr must carry the id and not the key: %q", errOut)
	}
	_, out2, _ := admin(t, nil, "kek", "generate")
	if out == out2 {
		t.Error("two keys are equal")
	}
}

func TestAdminTokenCreateAndRevoke(t *testing.T) {
	forEachBackend(t, func(t *testing.T, be adminBackend) {
		env := adminEnv(t, be, true)
		first := strings.TrimSpace(mustAdmin(t, env, "admin-token", "create"))
		if !strings.HasPrefix(first, tokens.AdminPrefix) {
			t.Fatalf("token = %q", first)
		}
		st := openStore(t, be, env)
		s, err := st.GetSetting(context.Background(), tokens.AdminSettingKey)
		if err != nil || s.Value != tokens.Hash(first) || strings.Contains(s.Value, first) {
			t.Fatalf("stored %q, %v; want only the hash", s.Value, err)
		}
		// Creating again replaces the token: the old hash is gone.
		second := strings.TrimSpace(mustAdmin(t, env, "admin-token", "create"))
		s, _ = st.GetSetting(context.Background(), tokens.AdminSettingKey)
		if second == first || s.Value != tokens.Hash(second) {
			t.Fatal("the old admin token still works")
		}
		mustAdmin(t, env, "admin-token", "revoke")
		if s, _ = st.GetSetting(context.Background(), tokens.AdminSettingKey); s.Value != "" {
			t.Fatalf("revoked value = %q", s.Value)
		}
		if code, _, _ := admin(t, env, "admin-token", "revoke"); code == exitOK {
			t.Error("revoking twice must fail")
		}
		// After a revoke a new token can be created.
		mustAdmin(t, env, "admin-token", "create")
	})
}
