package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "damson.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	agentEnv := []string{
		"DAMSON_AGENT__CONTROLLER_URL=wss://damson.example.com",
		"DAMSON_AGENT__TOKEN=s3cret",
	}
	tests := []struct {
		name    string
		role    Role
		file    string
		env     []string
		check   func(t *testing.T, c Config)
		wantErr []string // substrings, all must appear
	}{
		{
			name: "controller defaults", role: RoleController,
			check: func(t *testing.T, c Config) {
				if c.Controller.Listen != ":8080" || c.Database.Driver != DriverSQLite || c.Log.Level != "info" {
					t.Fatalf("unexpected defaults: %+v", c)
				}
			},
		},
		{
			name: "dev defaults", role: RoleDev,
		},
		{
			name: "file overrides defaults", role: RoleController,
			file: "[controller]\nlisten = \"127.0.0.1:9000\"\ninternal_url = \"http://c1:8081\"\ncluster_secret = \"" + strings.Repeat("s", 32) + "\"\n[database]\ndriver = \"postgres\"\ndsn = \"postgres://u@h/db\"\n",
			check: func(t *testing.T, c Config) {
				if c.Controller.Listen != "127.0.0.1:9000" || c.Database.Driver != DriverPostgres {
					t.Fatalf("file not applied: %+v", c)
				}
			},
		},
		{
			name: "insecure_dev and internal_url from env", role: RoleController,
			env: []string{"DAMSON_CONTROLLER__INSECURE_DEV=true", "DAMSON_CONTROLLER__INTERNAL_URL=http://c1:8080", "DAMSON_CONTROLLER__TRUST_FORWARDED_PROTO=true"},
			check: func(t *testing.T, c Config) {
				if !c.Controller.InsecureDev || !c.Controller.TrustForwardedProto || c.Controller.InternalURL != "http://c1:8080" {
					t.Fatalf("env not applied: %+v", c.Controller)
				}
			},
		},
		{
			name: "env overrides file", role: RoleController,
			file: "[controller]\nlisten = \"127.0.0.1:9000\"\n",
			env:  []string{"DAMSON_CONTROLLER__LISTEN=:7000", "DAMSON_LOG__LEVEL=debug", "UNRELATED=1"},
			check: func(t *testing.T, c Config) {
				if c.Controller.Listen != ":7000" || c.Log.Level != "debug" {
					t.Fatalf("env not applied: %+v", c)
				}
			},
		},
		{
			name: "agent slots default to 0 (auto)", role: RoleAgent, env: agentEnv,
			check: func(t *testing.T, c Config) {
				if c.Agent.Slots != 0 {
					t.Fatalf("slots = %d, want 0", c.Agent.Slots)
				}
			},
		},
		{
			name: "agent slots 0 is valid", role: RoleAgent,
			env: append([]string{"DAMSON_AGENT__SLOTS=0"}, agentEnv...),
			check: func(t *testing.T, c Config) {
				if c.Agent.Slots != 0 {
					t.Fatalf("slots = %d, want 0", c.Agent.Slots)
				}
			},
		},
		{
			name: "agent slots -1 is rejected", role: RoleAgent,
			env:     append([]string{"DAMSON_AGENT__SLOTS=-1"}, agentEnv...),
			wantErr: []string{"agent.slots"},
		},
		{
			name: "agent spool and host key settings", role: RoleAgent,
			env: append([]string{"DAMSON_AGENT__SPOOL_DIR=/var/spool/damson", "DAMSON_AGENT__SPOOL_MAX_BYTES=1048576", "DAMSON_AGENT__ALLOW_INSECURE_HOST_KEYS=true", "DAMSON_AGENT__TLS_CA_FILE=/etc/damson/ca.pem", "DAMSON_AGENT__ALLOW_INSECURE_WINRM=true"}, agentEnv...),
			check: func(t *testing.T, c Config) {
				a := c.Agent
				if a.SpoolDir != "/var/spool/damson" || a.SpoolMaxBytes != 1<<20 || !a.AllowInsecureHostKeys || a.TLSCAFile != "/etc/damson/ca.pem" || !a.AllowInsecureWinRM {
					t.Fatalf("unexpected agent config: %+v", a)
				}
			},
		},
		{
			name: "agent spool settings default to safe values", role: RoleAgent, env: agentEnv,
			check: func(t *testing.T, c Config) {
				if a := c.Agent; a.SpoolDir != "" || a.SpoolMaxBytes != 0 || a.AllowInsecureHostKeys || a.TLSCAFile != "" || a.AllowInsecureWinRM {
					t.Fatalf("unexpected defaults: %+v", a)
				}
			},
		},
		{
			name: "agent negative spool size is rejected", role: RoleAgent,
			env:     append([]string{"DAMSON_AGENT__SPOOL_MAX_BYTES=-5"}, agentEnv...),
			wantErr: []string{"agent.spool_max_bytes"},
		},
		{
			name: "agent bad drain grace is rejected", role: RoleAgent,
			env:     append([]string{"DAMSON_AGENT__DRAIN_GRACE=soon"}, agentEnv...),
			wantErr: []string{"agent.drain_grace"},
		},
		{
			name: "agent drain grace defaults to 20s", role: RoleAgent,
			env: agentEnv,
			check: func(t *testing.T, c Config) {
				if got := c.DrainGraceDuration(); got != 20*time.Second {
					t.Fatalf("drain grace = %s", got)
				}
			},
		},
		{
			name: "agent from env with int coercion", role: RoleAgent,
			env: append([]string{"DAMSON_AGENT__SLOTS=8"}, agentEnv...),
			check: func(t *testing.T, c Config) {
				if c.Agent.Slots != 8 || c.Agent.Pool != "default" {
					t.Fatalf("unexpected agent config: %+v", c.Agent)
				}
			},
		},
		{
			name: "agent missing required", role: RoleAgent,
			wantErr: []string{"agent.controller_url (DAMSON_AGENT__CONTROLLER_URL)", "agent.token (DAMSON_AGENT__TOKEN)"},
		},
		{
			name: "agent bad url and slots", role: RoleAgent,
			env:     []string{"DAMSON_AGENT__CONTROLLER_URL=http://x", "DAMSON_AGENT__TOKEN=t", "DAMSON_AGENT__SLOTS=-1"},
			wantErr: []string{"ws:// or wss://", "agent.slots"},
		},
		{
			name: "bad level reports all errors", role: RoleController,
			env:     []string{"DAMSON_LOG__LEVEL=loud", "DAMSON_CONTROLLER__LISTEN=nope", "DAMSON_DATABASE__DRIVER=mysql"},
			wantErr: []string{"log.level", "controller.listen", "database.driver"},
		},
		{
			name: "finally grace must be a duration", role: RoleController,
			env:     []string{"DAMSON_ENGINE__FINALLY_GRACE=soon"},
			wantErr: []string{"engine.finally_grace (DAMSON_ENGINE__FINALLY_GRACE)"},
		},
		{
			name: "finally grace must be positive", role: RoleDev,
			env:     []string{"DAMSON_ENGINE__FINALLY_GRACE=0s"},
			wantErr: []string{"engine.finally_grace"},
		},
		{
			name: "finally grace is bounded", role: RoleController,
			env:     []string{"DAMSON_ENGINE__FINALLY_GRACE=25h"},
			wantErr: []string{"engine.finally_grace"},
		},
		{
			name: "audit retention must not be negative", role: RoleController,
			env:     []string{"DAMSON_AUDIT__RETENTION_DAYS=-1"},
			wantErr: []string{"audit.retention_days"},
		},
		{
			name: "audit retention is bounded", role: RoleDev,
			env:     []string{"DAMSON_AUDIT__RETENTION_DAYS=40000"},
			wantErr: []string{"audit.retention_days"},
		},
		{
			name: "dev rejects postgres", role: RoleDev,
			env:     []string{"DAMSON_DATABASE__DRIVER=postgres"},
			wantErr: []string{"dev mode uses sqlite"},
		},
		{
			name: "unknown key is an error", role: RoleController,
			file:    "[controler]\nlisten = \":1\"\n",
			wantErr: []string{"controler"},
		},
		{
			name: "malformed file", role: RoleController,
			file:    "this is = = not toml",
			wantErr: []string{"reading config file"},
		},
		{
			name: "unknown role", role: Role("bogus"),
			wantErr: []string{"unknown role"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := ""
			if tt.file != "" {
				path = writeFile(t, tt.file)
			}
			c, err := Load(tt.role, path, tt.env)
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("expected error containing %v, got config %+v", tt.wantErr, c)
				}
				for _, s := range tt.wantErr {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q missing %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, c)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(RoleController, filepath.Join(t.TempDir(), "absent.toml"), nil)
	if err == nil || !strings.Contains(err.Error(), "absent.toml") {
		t.Fatalf("want error naming the file, got %v", err)
	}
}

func TestFilePath(t *testing.T) {
	env := []string{"DAMSON_CONFIG=/env.toml"}
	if got := FilePath("/flag.toml", env); got != "/flag.toml" {
		t.Errorf("flag should win, got %q", got)
	}
	if got := FilePath("", env); got != "/env.toml" {
		t.Errorf("env fallback, got %q", got)
	}
	if got := FilePath("", nil); got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

func TestConfigEnvVarIsNotAKey(t *testing.T) {
	if _, err := Load(RoleController, "", []string{"DAMSON_CONFIG=/x.toml"}); err != nil {
		t.Fatalf("DAMSON_CONFIG must not be parsed as a key: %v", err)
	}
}

// Secrets must not leak through the loggable form of the config.
func TestRedactedMasksSecrets(t *testing.T) {
	c := Config{
		Database:   Database{DSN: "postgres://u:hunter2@h/db"},
		Agent:      Agent{Token: "tok-123"},
		Controller: Controller{ClusterSecret: "cur", ClusterSecretPrevious: "prev"},
		Auth:       Auth{OIDC: OIDC{ClientSecret: "oidc-secret"}},
		Logs:       Logs{S3: S3Logs{SecretAccessKey: "s3-secret", SessionToken: "s3-token"}},
	}.Redacted()
	if c.Logs.S3.SecretAccessKey != "***" || c.Logs.S3.SessionToken != "***" {
		t.Fatalf("s3 log credentials not masked: %+v", c.Logs.S3)
	}
	if c.Controller.ClusterSecret != "***" || c.Controller.ClusterSecretPrevious != "***" {
		t.Fatalf("cluster secrets not masked: %+v", c.Controller)
	}
	if c.Auth.OIDC.ClientSecret != "***" {
		t.Fatalf("oidc client secret not masked: %+v", c.Auth.OIDC)
	}
	if c.Database.DSN != "***" || c.Agent.Token != "***" {
		t.Fatalf("secrets not masked: %+v", c)
	}
}

func TestLoadIgnoresNonKeyEnv(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want string // expected log.level
	}{
		{"KEK ignored", []string{"DAMSON_KEK=x"}, "info"},
		{"CONFIG ignored", []string{"DAMSON_CONFIG=/x.toml"}, "info"},
		{"both ignored, key overrides", []string{"DAMSON_KEK=x", "DAMSON_CONFIG=/x.toml", "DAMSON_LOG__LEVEL=debug"}, "debug"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(RoleController, "", tt.env)
			if err != nil {
				t.Fatal(err)
			}
			if c.Log.Level != tt.want {
				t.Fatalf("log.level = %q, want %q", c.Log.Level, tt.want)
			}
		})
	}
}

func TestUnknownFileKeyStillFails(t *testing.T) {
	p := writeFile(t, "[log]\nbogus = 1\n")
	if _, err := Load(RoleController, p, nil); err == nil {
		t.Fatal("unknown file key must be an error")
	}
}

func TestErrorsDoNotLeakSecrets(t *testing.T) {
	env := []string{
		"DAMSON_AGENT__CONTROLLER_URL=https://u:secret@host/?token=abc",
		"DAMSON_AGENT__TOKEN=agent-tok-xyz",
		"DAMSON_DATABASE__DRIVER=bogus",
		"DAMSON_DATABASE__DSN=postgres://u:dsnpass@h/db",
		"DAMSON_AGENT__SLOTS=-1",
	}
	_, err := Load(RoleAgent, "", env)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, leak := range []string{"secret", "token=abc", "agent-tok-xyz", "dsnpass"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error leaks %q: %v", leak, err)
		}
	}
	if !strings.Contains(err.Error(), "agent.controller_url") {
		t.Errorf("error should name the key: %v", err)
	}
}

func TestFinallyGrace(t *testing.T) {
	c, err := Load(RoleController, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.FinallyGraceDuration(); got != 2*time.Minute {
		t.Fatalf("default finally grace = %v; want 2m", got)
	}
	c, err = Load(RoleController, "", []string{"DAMSON_ENGINE__FINALLY_GRACE=45s"})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.FinallyGraceDuration(); got != 45*time.Second {
		t.Fatalf("finally grace = %v; want 45s", got)
	}
}

func TestClusterSecretValidation(t *testing.T) {
	good := strings.Repeat("k", 32)
	pg := []string{"DAMSON_DATABASE__DRIVER=postgres", "DAMSON_DATABASE__DSN=postgres://u@h/db"}
	withURL := append(slices.Clone(pg), "DAMSON_CONTROLLER__INTERNAL_URL=http://c1:8081")
	tests := []struct {
		name    string
		role    Role
		env     []string
		wantErr string
	}{
		{"sqlite needs none", RoleController, nil, ""},
		{"dev needs none", RoleDev, nil, ""},
		{"postgres with secret and url", RoleController, append(slices.Clone(withURL), "DAMSON_CONTROLLER__CLUSTER_SECRET="+good), ""},
		{"short secret", RoleController, append(slices.Clone(withURL), "DAMSON_CONTROLLER__CLUSTER_SECRET=short"), "at least 32 bytes"},
		{"short previous secret", RoleController, append(slices.Clone(withURL), "DAMSON_CONTROLLER__CLUSTER_SECRET="+good, "DAMSON_CONTROLLER__CLUSTER_SECRET_PREVIOUS=short"), "cluster_secret_previous"},
		{"secret needs an internal url", RoleController, append(slices.Clone(pg), "DAMSON_CONTROLLER__CLUSTER_SECRET="+good), "controller.internal_url"},
		{"internal url with credentials", RoleController, append(slices.Clone(pg), "DAMSON_CONTROLLER__CLUSTER_SECRET="+good, "DAMSON_CONTROLLER__INTERNAL_URL=http://u:p@c1:8081"), "controller.internal_url"},
		{"internal url with a path", RoleController, append(slices.Clone(pg), "DAMSON_CONTROLLER__CLUSTER_SECRET="+good, "DAMSON_CONTROLLER__INTERNAL_URL=http://c1:8081/x"), "controller.internal_url"},
		{"bad internal listen", RoleController, append(slices.Clone(withURL), "DAMSON_CONTROLLER__CLUSTER_SECRET="+good, "DAMSON_CONTROLLER__INTERNAL_LISTEN=nope"), "controller.internal_listen"},
		{"missing secret file", RoleController, append(slices.Clone(withURL), "DAMSON_CONTROLLER__CLUSTER_SECRET_FILE=/nonexistent/secret"), "cluster_secret_file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(tc.role, "", tc.env)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error = %v; want it to mention %q", err, tc.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), good) {
				t.Fatalf("error leaks the secret: %v", err)
			}
		})
	}
}

func TestClusterSecretFile(t *testing.T) {
	secret := strings.Repeat("f", 40)
	p := writeFile(t, secret+"\n")
	c, err := Load(RoleController, "", []string{"DAMSON_CONTROLLER__CLUSTER_SECRET_FILE=" + p, "DAMSON_CONTROLLER__INTERNAL_URL=http://c1:8081", "DAMSON_CONTROLLER__CLUSTER_SECRET_PREVIOUS=" + strings.Repeat("p", 32)})
	if err != nil {
		t.Fatal(err)
	}
	cur, prev, err := c.ClusterSecrets()
	if err != nil || cur != secret || prev != strings.Repeat("p", 32) {
		t.Fatalf("ClusterSecrets = %q, %q, %v", cur, prev, err)
	}
	// An explicit secret wins over the file.
	c.Controller.ClusterSecret = strings.Repeat("e", 32)
	if cur, _, _ := c.ClusterSecrets(); cur != strings.Repeat("e", 32) {
		t.Fatalf("explicit secret not preferred: %q", cur)
	}
}

func TestValidateServing(t *testing.T) {
	pg := []string{"DAMSON_DATABASE__DRIVER=postgres", "DAMSON_DATABASE__DSN=postgres://u@h/db"}
	c, err := Load(RoleController, "", pg) // admin commands load it too: no secret needed
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateServing(RoleController); err == nil || !strings.Contains(err.Error(), "controller.cluster_secret") {
		t.Fatalf("a postgres controller without a secret: %v", err)
	}
	if err := c.ValidateServing(RoleDev); err != nil {
		t.Fatalf("dev: %v", err)
	}
	sq, _ := Load(RoleController, "", nil)
	if err := sq.ValidateServing(RoleController); err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	c.Controller.ClusterSecret = strings.Repeat("k", 32)
	if err := c.ValidateServing(RoleController); err != nil {
		t.Fatalf("with a secret: %v", err)
	}
}

func TestValidateOIDC(t *testing.T) {
	ok := map[string]string{
		"AUTH__OIDC__ISSUER": "https://idp.example/realms/x", "AUTH__OIDC__CLIENT_ID": "damson", "AUTH__OIDC__CLIENT_SECRET": "s",
		"AUTH__OIDC__REDIRECT_URL": "https://damson.example/auth/callback",
	}
	load := func(over map[string]string) error {
		var env []string
		m := map[string]string{}
		for k, v := range ok {
			m[k] = v
		}
		for k, v := range over {
			m[k] = v
		}
		for k, v := range m {
			env = append(env, "DAMSON_"+k+"="+v)
		}
		_, err := Load(RoleController, "", env)
		return err
	}
	if err := load(nil); err != nil {
		t.Fatalf("valid oidc config: %v", err)
	}
	tests := []struct {
		name string
		over map[string]string
		want string
	}{
		{"no client id", map[string]string{"AUTH__OIDC__CLIENT_ID": ""}, "auth.oidc.client_id"},
		{"no secret", map[string]string{"AUTH__OIDC__CLIENT_SECRET": ""}, "auth.oidc.client_secret"},
		{"missing secret file", map[string]string{"AUTH__OIDC__CLIENT_SECRET": "", "AUTH__OIDC__CLIENT_SECRET_FILE": "/nonexistent"}, "client_secret_file"},
		{"bad issuer", map[string]string{"AUTH__OIDC__ISSUER": "idp"}, "auth.oidc.issuer"},
		{"redirect not the callback", map[string]string{"AUTH__OIDC__REDIRECT_URL": "https://damson.example/"}, "auth.oidc.redirect_url"},
		{"plain http issuer", map[string]string{"AUTH__OIDC__ISSUER": "http://idp.example/realms/x"}, "auth.oidc.issuer"},
		{"plain http redirect", map[string]string{"AUTH__OIDC__REDIRECT_URL": "http://damson.example/auth/callback"}, "auth.oidc.redirect_url"},
		{"empty groups claim", map[string]string{"AUTH__OIDC__GROUPS_CLAIM": ""}, "auth.oidc.groups_claim"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := load(tt.over); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v; want it to name %s", err, tt.want)
			}
		})
	}
	// Plain http is for development: cookie_insecure or insecure_dev allow it.
	for _, key := range []string{"AUTH__COOKIE_INSECURE", "CONTROLLER__INSECURE_DEV"} {
		if err := load(map[string]string{key: "true", "AUTH__OIDC__ISSUER": "http://localhost:8180/realms/x", "AUTH__OIDC__REDIRECT_URL": "http://localhost:8080/auth/callback"}); err != nil {
			t.Fatalf("http with %s: %v", key, err)
		}
	}
	// Without an issuer nothing else is checked.
	if err := load(map[string]string{"AUTH__OIDC__ISSUER": "", "AUTH__OIDC__CLIENT_ID": ""}); err != nil {
		t.Fatalf("oidc off: %v", err)
	}
}
