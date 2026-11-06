package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/denyszorinets/damson/internal/secrets"
	secretsdb "github.com/denyszorinets/damson/internal/secrets/db"
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

func TestAdminRotateKEK(t *testing.T) {
	forEachBackend(t, func(t *testing.T, be adminBackend) {
		ctx := context.Background()
		env := adminEnv(t, be, true)
		oldK, newK := genKEK(t), genKEK(t)
		st := openStore(t, be, env)
		oldRing, err := secrets.NewKeyring(oldK, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := secretsdb.CheckKEK(ctx, st, oldRing, rand.Reader); err != nil {
			t.Fatal(err)
		}
		b := secretsdb.New(st, oldRing, rand.Reader, time.Now, func() string { return uuid.NewString() })
		if _, _, err := b.Put(ctx, "proj", "db", "password", map[string]string{"username": "u", "password": "pw-VALUE"}, nil); err != nil {
			t.Fatal(err)
		}

		if code, _, errOut := admin(t, env, "rotate-kek"); code == exitOK || !strings.Contains(errOut, "DAMSON_KEK") {
			t.Fatalf("without a key: exit %d, %q", code, errOut)
		}
		both := append(append([]string{}, env...), "DAMSON_KEK="+newK, "DAMSON_KEK_PREVIOUS="+oldK)
		out := mustAdmin(t, both, "rotate-kek")
		if !strings.Contains(out, "1 version(s) re-wrapped") || !strings.Contains(out, "canary") {
			t.Fatalf("output = %q", out)
		}
		for _, leak := range []string{oldK, newK, "pw-VALUE"} {
			if strings.Contains(out, leak) {
				t.Fatalf("output leaks %q", leak)
			}
		}
		newRing, _ := secrets.NewKeyring(newK, "")
		got, err := secretsdb.New(st, newRing, rand.Reader, time.Now, nil).Get(ctx, "proj", "db")
		if err != nil || got["password"] != "pw-VALUE" {
			t.Fatalf("read with the new key alone = %v, %v", got, err)
		}
		// A second run is a no-op.
		if out := mustAdmin(t, both, "rotate-kek", "--json"); !strings.Contains(out, `"rewrapped": 0`) || !strings.Contains(out, `"canary_moved": false`) {
			t.Fatalf("second run = %q", out)
		}
	})
}

func genKEK(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}
