package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Validate checks the configuration for role and returns every problem found,
// joined, each naming the config key and its environment variable.
func (c Config) Validate(role Role) error {
	var errs []error
	add := func(key, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s (%s): %s", key, envName(key), fmt.Sprintf(format, args...)))
	}

	switch role {
	case RoleController, RoleAgent, RoleDev:
	default:
		return fmt.Errorf("unknown role %q (want controller, agent or dev)", role)
	}

	var lvl = c.Log.Level
	if lvl != "debug" && lvl != "info" && lvl != "warn" && lvl != "error" {
		add("log.level", "must be one of debug, info, warn, error; got %q", lvl)
	}

	if role == RoleController || role == RoleDev {
		c.validateDatabase(role, add)
		if _, port, err := net.SplitHostPort(c.Controller.Listen); err != nil || port == "" {
			add("controller.listen", "must be host:port such as \":8080\"; got %q", c.Controller.Listen)
		}
	}
	if role == RoleAgent {
		c.validateAgent(add)
	}

	if len(errs) == 0 {
		return nil
	}
	return joinLines(errs)
}

func (c Config) validateDatabase(role Role, add func(key, format string, args ...any)) {
	switch c.Database.Driver {
	case DriverSQLite:
	case DriverPostgres:
		if role == RoleDev {
			add("database.driver", "dev mode uses sqlite; got %q", c.Database.Driver)
		}
	default:
		add("database.driver", "must be sqlite or postgres; got %q", c.Database.Driver)
	}
	if c.Database.DSN == "" {
		add("database.dsn", "is required (for example \"file:damson.db\" or a postgres:// URL)")
	}
}

func (c Config) validateAgent(add func(key, format string, args ...any)) {
	u, err := url.Parse(c.Agent.ControllerURL)
	switch {
	case c.Agent.ControllerURL == "":
		add("agent.controller_url", "is required, for example wss://damson.example.com")
	case err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "":
		add("agent.controller_url", "must be a ws:// or wss:// URL with a host")
	}
	if c.Agent.Token == "" {
		add("agent.token", "is required (the pool token issued by the controller)")
	}
	if c.Agent.Pool == "" {
		add("agent.pool", "must not be empty")
	}
	if c.Agent.Slots < 1 {
		add("agent.slots", "must be at least 1; got %d", c.Agent.Slots)
	}
}

// envName maps a dotted key to its environment variable.
func envName(key string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(key, ".", "__"))
}

// joinLines joins errors so each prints on its own indented line.
func joinLines(errs []error) error {
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Error()
	}
	return errors.New("invalid configuration:\n  - " + strings.Join(msgs, "\n  - "))
}
