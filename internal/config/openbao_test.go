package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSecretsBackend(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tokenFile := write("token", "s.abc\n")
	emptyFile := write("empty", "\n")
	ob := func(f func(*Config)) Config {
		c := Config{Secrets: Secrets{Backend: SecretsOpenBao, OpenBao: OpenBao{
			Address: "https://bao.example.com:8200", Mount: "secret", Prefix: "damson", RequestTimeout: "10s", CacheTTL: "0s",
			Auth: OpenBaoAuth{Method: "token", TokenFile: tokenFile, ServiceAccountTokenFile: DefaultServiceAccountTokenFile},
		}}}
		f(&c)
		return c
	}
	tests := []struct {
		name string
		role Role
		c    Config
		want string
	}{
		{"db", RoleController, Config{Secrets: Secrets{Backend: SecretsDB}}, ""},
		{"unknown backend", RoleController, Config{Secrets: Secrets{Backend: "vault"}}, "secrets.backend"},
		{"token file", RoleController, ob(func(*Config) {}), ""},
		{"dev refuses", RoleDev, ob(func(*Config) {}), "secrets.backend"},
		{"kek refused", RoleController, ob(func(c *Config) { c.Secrets.KEK = "k" }), "secrets.backend"},
		{"plain http", RoleController, ob(func(c *Config) { c.Secrets.OpenBao.Address = "http://bao:8200" }), "secrets.openbao.address"},
		{"plain http allowed", RoleController, ob(func(c *Config) {
			c.Secrets.OpenBao.Address = "http://bao:8200"
			c.Secrets.OpenBao.AllowPlainHTTP = true
		}), ""},
		{"empty prefix", RoleController, ob(func(c *Config) { c.Secrets.OpenBao.Prefix = "" }), "secrets.openbao.prefix"},
		{"bad mount", RoleController, ob(func(c *Config) { c.Secrets.OpenBao.Mount = "a/../b" }), "secrets.openbao.mount"},
		{"bad auth mount", RoleController, ob(func(c *Config) { c.Secrets.OpenBao.Auth.Mount = "x/../y" }), "auth.mount"},
		{"address with credentials", RoleController, ob(func(c *Config) { c.Secrets.OpenBao.Address = "https://u:p@bao:8200" }), "secrets.openbao.address"},
		{"cache ttl over max", RoleController, ob(func(c *Config) { c.Secrets.OpenBao.CacheTTL = "1m" }), "secrets.openbao.cache_ttl"},
		{"cache ttl ok", RoleController, ob(func(c *Config) { c.Secrets.OpenBao.CacheTTL = "30s" }), ""},
		{"token env and file", RoleController, ob(func(c *Config) { c.Secrets.OpenBao.Token = "t" }), "token_file"},
		{"token env only", RoleController, ob(func(c *Config) { c.Secrets.OpenBao.Token = "t"; c.Secrets.OpenBao.Auth.TokenFile = "" }), ""},
		{"empty token file", RoleController, ob(func(c *Config) { c.Secrets.OpenBao.Auth.TokenFile = emptyFile }), "token_file"},
		{"unknown method", RoleController, ob(func(c *Config) { c.Secrets.OpenBao.Auth.Method = "ldap" }), "auth.method"},
		{"approle complete", RoleController, ob(func(c *Config) {
			a := &c.Secrets.OpenBao.Auth
			*a = OpenBaoAuth{Method: "approle", RoleID: "r", SecretIDFile: tokenFile, ServiceAccountTokenFile: DefaultServiceAccountTokenFile}
		}), ""},
		{"approle without secret id", RoleController, ob(func(c *Config) {
			c.Secrets.OpenBao.Auth = OpenBaoAuth{Method: "approle", RoleID: "r", ServiceAccountTokenFile: DefaultServiceAccountTokenFile}
		}), "secret_id"},
		{"approle with foreign token", RoleController, ob(func(c *Config) {
			c.Secrets.OpenBao.Auth = OpenBaoAuth{Method: "approle", RoleID: "r", SecretIDFile: tokenFile, TokenFile: tokenFile, ServiceAccountTokenFile: DefaultServiceAccountTokenFile}
		}), "auth.method"},
		{"kubernetes", RoleController, ob(func(c *Config) {
			c.Secrets.OpenBao.Auth = OpenBaoAuth{Method: "kubernetes", Role: "damson", ServiceAccountTokenFile: DefaultServiceAccountTokenFile}
		}), ""},
		{"kubernetes without role", RoleController, ob(func(c *Config) {
			c.Secrets.OpenBao.Auth = OpenBaoAuth{Method: "kubernetes", ServiceAccountTokenFile: DefaultServiceAccountTokenFile}
		}), "auth.role"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			tc.c.validateSecretsBackend(tc.role, func(key, _ string, _ ...any) { got = append(got, key) })
			switch {
			case tc.want == "" && len(got) != 0:
				t.Fatalf("errors %v, want none", got)
			case tc.want != "" && (len(got) == 0 || !strings.Contains(strings.Join(got, ","), tc.want)):
				t.Fatalf("errors %v, want %q", got, tc.want)
			}
		})
	}
}

func TestOpenBaoCredentialsComeFromTheEnvironment(t *testing.T) {
	var c Config
	c.loadOpenBao([]string{EnvOpenBaoToken + "=s.tok ", EnvOpenBaoSecretID + "=sid", "OTHER=1"})
	if c.Secrets.OpenBao.Token != "s.tok" || c.Secrets.OpenBao.SecretID != "sid" {
		t.Fatalf("%+v", c.Secrets.OpenBao)
	}
	r := Config{Secrets: Secrets{OpenBao: OpenBao{Token: "s.tok", SecretID: "sid", Auth: OpenBaoAuth{RoleID: "rid"}}}}.Redacted()
	if r.Secrets.OpenBao.Token != "***" || r.Secrets.OpenBao.SecretID != "***" || r.Secrets.OpenBao.Auth.RoleID != "***" {
		t.Fatalf("not redacted: %+v", r.Secrets.OpenBao)
	}
}
