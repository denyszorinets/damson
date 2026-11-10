package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

func TestMetricsTokenCreateAndRevoke(t *testing.T) {
	forEachBackend(t, func(t *testing.T, be adminBackend) {
		env := adminEnv(t, be, true)
		first := strings.TrimSpace(mustAdmin(t, env, "metrics-token", "create"))
		if !strings.HasPrefix(first, tokens.MetricsPrefix) {
			t.Fatalf("token = %q", first)
		}
		st := openStore(t, be, env)
		s, err := st.GetSetting(context.Background(), tokens.MetricsSettingKey)
		if err != nil || s.Value != tokens.Hash(first) || strings.Contains(s.Value, first) {
			t.Fatalf("stored %q, %v; want only the hash", s.Value, err)
		}
		if _, err := st.GetSetting(context.Background(), tokens.AdminSettingKey); err == nil {
			t.Fatal("creating a metrics token touched the admin token")
		}
		second := strings.TrimSpace(mustAdmin(t, env, "metrics-token", "create"))
		if s, _ = st.GetSetting(context.Background(), tokens.MetricsSettingKey); second == first || s.Value != tokens.Hash(second) {
			t.Fatal("the old metrics token still works")
		}
		mustAdmin(t, env, "metrics-token", "revoke")
		if s, _ = st.GetSetting(context.Background(), tokens.MetricsSettingKey); s.Value != "" {
			t.Fatalf("revoked value = %q", s.Value)
		}
		if code, _, _ := admin(t, env, "metrics-token", "revoke"); code == exitOK {
			t.Error("revoking twice must fail")
		}
		evs, err := st.ListAuditEvents(context.Background(), store.AuditFilter{TargetType: store.AuditTargetInstance}, 10)
		if err != nil || len(evs) != 3 {
			t.Fatalf("events = %+v, %v; want create, create, revoke", evs, err)
		}
		for _, e := range evs {
			if e.Target != "metrics_token" || string(e.Details) != "{}" {
				t.Errorf("event %+v", e)
			}
		}
	})
}
