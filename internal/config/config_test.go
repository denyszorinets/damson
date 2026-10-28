package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
			file: "[controller]\nlisten = \"127.0.0.1:9000\"\n[database]\ndriver = \"postgres\"\ndsn = \"postgres://u@h/db\"\n",
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
			env: append([]string{"DAMSON_AGENT__SPOOL_DIR=/var/spool/damson", "DAMSON_AGENT__SPOOL_MAX_BYTES=1048576", "DAMSON_AGENT__ALLOW_INSECURE_HOST_KEYS=true"}, agentEnv...),
			check: func(t *testing.T, c Config) {
				a := c.Agent
				if a.SpoolDir != "/var/spool/damson" || a.SpoolMaxBytes != 1<<20 || !a.AllowInsecureHostKeys {
					t.Fatalf("unexpected agent config: %+v", a)
				}
			},
		},
		{
			name: "agent spool settings default to safe values", role: RoleAgent, env: agentEnv,
			check: func(t *testing.T, c Config) {
				if a := c.Agent; a.SpoolDir != "" || a.SpoolMaxBytes != 0 || a.AllowInsecureHostKeys {
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
		Database: Database{DSN: "postgres://u:hunter2@h/db"},
		Agent:    Agent{Token: "tok-123"},
	}.Redacted()
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
