// Package config loads and validates Damson configuration.
//
// Sources are applied in increasing precedence: built-in defaults, an optional
// TOML file, then DAMSON_* environment variables. See docs/reference/configuration.rst.
package config

import (
	"log/slog"
	"time"
)

// Role selects which parts of the configuration are required.
type Role string

// Roles a process can run as.
const (
	RoleController Role = "controller"
	RoleAgent      Role = "agent"
	RoleDev        Role = "dev"
)

// Database drivers.
const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
)

// Config is the full typed configuration.
type Config struct {
	Log        Log        `koanf:"log"`
	Database   Database   `koanf:"database"`
	Controller Controller `koanf:"controller"`
	Engine     Engine     `koanf:"engine"`
	Audit      Audit      `koanf:"audit"`
	Agent      Agent      `koanf:"agent"`
	Secrets    Secrets    `koanf:"secrets"`
}

// Log configures the slog JSON logger.
type Log struct {
	Level string `koanf:"level"` // debug, info, warn, error
}

// Database selects the store backend.
type Database struct {
	Driver string `koanf:"driver"` // sqlite or postgres
	DSN    string `koanf:"dsn"`    // may embed a password; never log it
}

// Controller configures the controller role.
type Controller struct {
	Listen string `koanf:"listen"` // host:port for the HTTP API and agent sockets
	// InsecureDev allows plain ws:// agent connections (no TLS and no
	// X-Forwarded-Proto: https). Development only: the pool token crosses the
	// wire in clear text. Always on for the dev role.
	InsecureDev bool `koanf:"insecure_dev"`
	// InternalURL is where other controllers reach this one (ARCHITECTURE 8.5).
	InternalURL string `koanf:"internal_url"`
	// InternalListen is the host:port of the internal listener that serves other
	// controllers (log tail proxy). It starts only when a cluster secret is
	// configured; the dev role never starts it.
	InternalListen string `koanf:"internal_listen"`
	// ClusterSecret authenticates controllers to each other (HMAC, ADR-0040);
	// at least MinClusterSecretBytes. ClusterSecretFile is read instead when
	// the secret itself is empty. ClusterSecretPrevious is also accepted when
	// verifying, for rotation. All three are secrets.
	ClusterSecret         string `koanf:"cluster_secret"`
	ClusterSecretFile     string `koanf:"cluster_secret_file"`
	ClusterSecretPrevious string `koanf:"cluster_secret_previous"`
	// TrustForwardedProto honors X-Forwarded-Proto: https from a TLS-terminating
	// proxy when deciding whether an agent connection is secure.
	TrustForwardedProto bool `koanf:"trust_forwarded_proto"`
}

// Engine configures the orchestration engine (controller and dev roles). Every
// controller of a cluster must use the same values: its decisions are shared
// through the database.
type Engine struct {
	// FinallyGrace is how long finally blocks may keep running after a cancel
	// or the job-level timeout, as a Go duration. Default 2m.
	FinallyGrace string `koanf:"finally_grace"`
}

// Audit configures the audit trail (controller and dev roles).
type Audit struct {
	// RetentionDays is how long audit events are kept. 0 keeps them forever
	// (the default); a positive value lets the retention duty delete older
	// events (ADR-0054).
	RetentionDays int `koanf:"retention_days"`
}

// MaxAuditRetentionDays bounds audit.retention_days (100 years).
const MaxAuditRetentionDays = 36500

// MaxFinallyGrace bounds engine.finally_grace.
const MaxFinallyGrace = 24 * time.Hour

// FinallyGraceDuration returns the configured grace. Validate guarantees it
// parses; an unset or invalid value gives zero, which selects the engine's
// default (engine.DefaultFinallyGrace, the same 2m as the key's default).
func (c Config) FinallyGraceDuration() time.Duration {
	d, err := time.ParseDuration(c.Engine.FinallyGrace)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// Agent configures the agent role.
type Agent struct {
	ControllerURL string `koanf:"controller_url"` // ws:// or wss:// URL
	Token         string `koanf:"token"`          // pool token; secret
	Pool          string `koanf:"pool"`
	Slots         int    `koanf:"slots"` // concurrent executions; 0 means CPU count x 4
	// SpoolDir holds the bounded log spool file; empty means the system temp dir.
	SpoolDir string `koanf:"spool_dir"`
	// SpoolMaxBytes bounds the log spool; 0 means 256 MiB.
	SpoolMaxBytes int64 `koanf:"spool_max_bytes"`
	// AllowInsecureHostKeys lets the ssh plugin accept nodes whose host key
	// policy is "insecure". Off by default.
	AllowInsecureHostKeys bool `koanf:"allow_insecure_host_keys"`
	// TLSCAFile is a PEM file of extra CA certificates the transfer plugin
	// trusts for ftps, in addition to the system roots. Empty means system
	// roots only. Certificate verification cannot be turned off.
	TLSCAFile string `koanf:"tls_ca_file"`
	// AllowInsecureWinRM lets the winrm plugin use transport "http" and
	// insecure_skip_verify. Off by default; a private CA belongs in TLSCAFile.
	AllowInsecureWinRM bool `koanf:"allow_insecure_winrm"`
	// AllowPlainFTP lets the transfer plugin use protocol "ftp", which sends
	// the password and the data in clear text. Off by default; use ftps.
	AllowPlainFTP bool `koanf:"allow_plain_ftp"`
	// AllowPlainS3 lets the transfer plugin use an s3 endpoint over plain
	// http (credentials and data in clear text). Off by default; use https.
	AllowPlainS3 bool `koanf:"allow_plain_s3"`
}

// SlogLevel returns the configured level. Validate guarantees it parses.
func (c Config) SlogLevel() slog.Level {
	var l slog.Level
	_ = l.UnmarshalText([]byte(c.Log.Level))
	return l
}

// Redacted returns a copy that is safe to log: secrets are replaced.
func (c Config) Redacted() Config {
	c.Database.DSN = redact(c.Database.DSN)
	c.Agent.Token = redact(c.Agent.Token)
	c.Controller.ClusterSecret = redact(c.Controller.ClusterSecret)
	c.Controller.ClusterSecretPrevious = redact(c.Controller.ClusterSecretPrevious)
	c.Secrets.KEK = redact(c.Secrets.KEK)
	c.Secrets.KEKPrevious = redact(c.Secrets.KEKPrevious)
	return c
}

func redact(s string) string {
	if s == "" {
		return ""
	}
	return "***"
}

func defaults() map[string]any {
	return map[string]any{
		"log.level":                          "info",
		"database.driver":                    DriverSQLite,
		"database.dsn":                       "file:damson.db",
		"controller.listen":                  ":8080",
		"controller.insecure_dev":            false,
		"controller.internal_url":            "",
		"controller.internal_listen":         ":8081",
		"controller.cluster_secret":          "",
		"controller.cluster_secret_file":     "",
		"controller.cluster_secret_previous": "",
		"secrets.kek_file":                   "",
		"secrets.kek_previous_file":          "",
		"controller.trust_forwarded_proto":   false,
		"engine.finally_grace":               "2m",
		"audit.retention_days":               0,
		"agent.controller_url":               "",
		"agent.token":                        "",
		"agent.pool":                         "default",
		"agent.slots":                        0,
		"agent.spool_dir":                    "",
		"agent.spool_max_bytes":              0,
		"agent.allow_insecure_host_keys":     false,
		"agent.tls_ca_file":                  "",
		"agent.allow_insecure_winrm":         false,
		"agent.allow_plain_ftp":              false,
	}
}
