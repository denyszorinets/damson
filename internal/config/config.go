// Package config loads and validates Damson configuration.
//
// Sources are applied in increasing precedence: built-in defaults, an optional
// TOML file, then DAMSON_* environment variables. See docs/reference/configuration.rst.
package config

import "log/slog"

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
	Agent      Agent      `koanf:"agent"`
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
	// TrustForwardedProto honors X-Forwarded-Proto: https from a TLS-terminating
	// proxy when deciding whether an agent connection is secure.
	TrustForwardedProto bool `koanf:"trust_forwarded_proto"`
}

// Agent configures the agent role.
type Agent struct {
	ControllerURL string `koanf:"controller_url"` // ws:// or wss:// URL
	Token         string `koanf:"token"`          // pool token; secret
	Pool          string `koanf:"pool"`
	Slots         int    `koanf:"slots"` // concurrent executions
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
		"log.level":                        "info",
		"database.driver":                  DriverSQLite,
		"database.dsn":                     "file:damson.db",
		"controller.listen":                ":8080",
		"controller.insecure_dev":          false,
		"controller.internal_url":          "",
		"controller.trust_forwarded_proto": false,
		"agent.controller_url":             "",
		"agent.token":                      "",
		"agent.pool":                       "default",
		"agent.slots":                      4,
	}
}
