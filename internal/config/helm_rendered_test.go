package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestHelmRenderedConfig loads the config.toml the Helm chart renders for each of
// its ci/ values files, with the environment the chart gives the controller.
// deploy/helm/check.sh sets DAMSON_HELM_RENDERED to the folder of rendered files.
func TestHelmRenderedConfig(t *testing.T) {
	dir := os.Getenv("DAMSON_HELM_RENDERED")
	if dir == "" {
		t.Skip("DAMSON_HELM_RENDERED is not set (run deploy/helm/check.sh)")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no rendered config in %s: %v", dir, err)
	}
	mounted := regexp.MustCompile(`"/etc/damson/[^"]+"`)
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			// Paths the chart mounts into the pod exist only there: make them here.
			root := t.TempDir()
			text := mounted.ReplaceAllStringFunc(string(raw), func(q string) string {
				p := filepath.Join(root, strings.Trim(q, `"`))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("value\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return `"` + p + `"`
			})
			cfgPath := filepath.Join(root, "config.toml")
			if err := os.WriteFile(cfgPath, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			// What the chart's Secrets provide as environment variables.
			env := []string{
				"DAMSON_DATABASE__DSN=postgres://u:p@h/db",
				"DAMSON_CONTROLLER__CLUSTER_SECRET=0123456789012345678901234567890123456789",
				"DAMSON_CONTROLLER__INTERNAL_URL=http://10.0.0.1:8081",
			}
			if strings.Contains(text, `backend = "openbao"`) {
				if strings.Contains(text, `method = "approle"`) {
					env = append(env, "DAMSON_OPENBAO_SECRET_ID=sid") // the role id file is mounted; the secret id is an env var
				}
			} else {
				env = append(env, "DAMSON_KEK=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
			}
			if strings.Contains(text, "[auth.oidc]") {
				env = append(env, "DAMSON_AUTH__OIDC__CLIENT_SECRET=s")
			}
			if strings.Contains(text, "[logs.s3]") {
				env = append(env, "DAMSON_LOGS__S3__ACCESS_KEY_ID=a", "DAMSON_LOGS__S3__SECRET_ACCESS_KEY=b")
			}
			if _, err := Load(RoleController, cfgPath, env); err != nil {
				t.Fatalf("the chart renders a configuration the controller refuses: %v", err)
			}
		})
	}
}
