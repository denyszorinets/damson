package config

import (
	"net/url"
	"os"
	"strings"
	"time"
)

// Environment variables that carry OpenBao credentials (ADR-0068). Like the
// KEK they are not configuration keys and never appear in a TOML file.
const (
	EnvOpenBaoToken    = "DAMSON_OPENBAO_TOKEN"
	EnvOpenBaoSecretID = "DAMSON_OPENBAO_SECRET_ID"
)

// Secret backend names (secrets.backend).
const (
	SecretsDB      = "db"
	SecretsOpenBao = "openbao"
)

// MaxOpenBaoCacheTTL bounds secrets.openbao.cache_ttl.
const MaxOpenBaoCacheTTL = 30 * time.Second

// OpenBao configures the openbao secret backend (ARCHITECTURE 9.2).
type OpenBao struct {
	Address        string `koanf:"address"`
	Namespace      string `koanf:"namespace"`
	Mount          string `koanf:"mount"`
	Prefix         string `koanf:"prefix"`
	ReadOnly       bool   `koanf:"read_only"`
	TLSCAFile      string `koanf:"tls_ca_file"`
	AllowPlainHTTP bool   `koanf:"allow_plain_http"`
	RequestTimeout string `koanf:"request_timeout"`
	CacheTTL       string `koanf:"cache_ttl"`

	Auth OpenBaoAuth `koanf:"auth"`

	// Token and SecretID come from the environment; Load fills them.
	Token, SecretID string `koanf:"-"`
}

// OpenBaoAuth selects how the controller authenticates.
type OpenBaoAuth struct {
	Method                  string `koanf:"method"` // token, approle or kubernetes
	TokenFile               string `koanf:"token_file"`
	RoleID                  string `koanf:"role_id"`
	RoleIDFile              string `koanf:"role_id_file"`
	SecretIDFile            string `koanf:"secret_id_file"`
	Role                    string `koanf:"role"`
	Mount                   string `koanf:"mount"`
	ServiceAccountTokenFile string `koanf:"service_account_token_file"`
}

// DefaultServiceAccountTokenFile is where Kubernetes mounts the pod's token.
const DefaultServiceAccountTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// saTokenSet reports whether the service account token file was changed from
// its default, which only the kubernetes method uses.
func (a OpenBaoAuth) saTokenSet() bool {
	return a.ServiceAccountTokenFile != "" && a.ServiceAccountTokenFile != DefaultServiceAccountTokenFile
}

func envValue(environ []string, name string) string {
	var v string
	for _, kv := range environ {
		if x, ok := strings.CutPrefix(kv, name+"="); ok {
			v = strings.TrimSpace(x)
		}
	}
	return v
}

// loadOpenBao reads the credential variables.
func (c *Config) loadOpenBao(environ []string) {
	c.Secrets.OpenBao.Token = envValue(environ, EnvOpenBaoToken)
	c.Secrets.OpenBao.SecretID = envValue(environ, EnvOpenBaoSecretID)
}

// OpenBaoRequestTimeout returns secrets.openbao.request_timeout; Validate
// guarantees it parses.
func (c Config) OpenBaoRequestTimeout() time.Duration {
	d, _ := time.ParseDuration(c.Secrets.OpenBao.RequestTimeout)
	return d
}

// OpenBaoCacheTTL returns secrets.openbao.cache_ttl; Validate guarantees it parses.
func (c Config) OpenBaoCacheTTL() time.Duration {
	d, _ := time.ParseDuration(c.Secrets.OpenBao.CacheTTL)
	return d
}

func (c Config) validateSecretsBackend(role Role, add func(key, format string, args ...any)) {
	s := c.Secrets
	switch s.Backend {
	case SecretsDB:
		return
	case SecretsOpenBao:
	default:
		add("secrets.backend", "must be db or openbao; got %q", s.Backend)
		return
	}
	if role == RoleDev {
		add("secrets.backend", "damson dev always uses the db backend")
	}
	if s.KEK != "" || s.KEKFile != "" || s.KEKPrevious != "" || s.KEKPreviousFile != "" {
		add("secrets.backend", "the openbao backend takes no KEK: unset %s, secrets.kek_file and the previous-key settings", EnvKEK)
	}
	o := s.OpenBao
	if u, err := url.Parse(o.Address); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		add("secrets.openbao.address", "must be an https URL such as https://bao.example.com:8200")
	} else if u.Scheme == "http" && !o.AllowPlainHTTP {
		add("secrets.openbao.address", "is plain http; use https, or set secrets.openbao.allow_plain_http")
	}
	if u, err := url.Parse(o.Address); err == nil && u.User != nil {
		add("secrets.openbao.address", "must not carry credentials")
	}
	if o.Prefix == "" {
		add("secrets.openbao.prefix", "must not be empty: Damson would write into the root of a shared mount")
	} else if !validKVPath(o.Prefix) {
		add("secrets.openbao.prefix", "must be 1 to 8 segments of letters, digits, . _ - such as damson/prod")
	}
	if !validKVPath(o.Mount) {
		add("secrets.openbao.mount", "must be 1 to 8 segments of letters, digits, . _ - such as secret")
	}
	if o.Auth.Mount != "" && !validKVPath(o.Auth.Mount) {
		add("secrets.openbao.auth.mount", "must be 1 to 8 segments of letters, digits, . _ - such as approle")
	}
	if d, err := time.ParseDuration(o.RequestTimeout); err != nil || d <= 0 || d > time.Minute {
		add("secrets.openbao.request_timeout", "must be a duration up to 1m such as \"10s\"; got %q", o.RequestTimeout)
	}
	if d, err := time.ParseDuration(o.CacheTTL); err != nil || d < 0 || d > MaxOpenBaoCacheTTL {
		add("secrets.openbao.cache_ttl", "must be a duration from 0 to %s; got %q", MaxOpenBaoCacheTTL, o.CacheTTL)
	}
	if o.TLSCAFile != "" {
		if _, err := os.Stat(o.TLSCAFile); err != nil {
			add("secrets.openbao.tls_ca_file", "%v", err)
		}
	}
	c.validateOpenBaoAuth(add)
}

func (c Config) validateOpenBaoAuth(add func(key, format string, args ...any)) {
	o := c.Secrets.OpenBao
	a := o.Auth
	switch a.Method {
	case "token":
		if a.TokenFile == "" && o.Token == "" {
			add("secrets.openbao.auth.token_file", "is required for method token (or set %s)", EnvOpenBaoToken)
		}
		if a.TokenFile != "" && o.Token != "" {
			add("secrets.openbao.auth.token_file", "and %s are both set; use one", EnvOpenBaoToken)
		}
		c.rejectOtherAuth(add, "token", a.RoleID != "" || a.RoleIDFile != "" || a.SecretIDFile != "" || o.SecretID != "" || a.Role != "" || a.saTokenSet())
	case "approle":
		if (a.RoleID == "") == (a.RoleIDFile == "") {
			add("secrets.openbao.auth.role_id", "set exactly one of role_id and role_id_file for method approle")
		}
		if (a.SecretIDFile == "") == (o.SecretID == "") {
			add("secrets.openbao.auth.secret_id_file", "set exactly one of secret_id_file and %s for method approle", EnvOpenBaoSecretID)
		}
		c.rejectOtherAuth(add, "approle", a.TokenFile != "" || o.Token != "" || a.Role != "" || a.saTokenSet())
	case "kubernetes":
		if a.Role == "" {
			add("secrets.openbao.auth.role", "is required for method kubernetes")
		}
		c.rejectOtherAuth(add, "kubernetes", a.TokenFile != "" || o.Token != "" || a.RoleID != "" || a.RoleIDFile != "" || a.SecretIDFile != "" || o.SecretID != "")
	default:
		add("secrets.openbao.auth.method", "must be token, approle or kubernetes; got %q", a.Method)
		return
	}
	for _, f := range []struct{ key, path string }{
		{"token_file", a.TokenFile}, {"role_id_file", a.RoleIDFile}, {"secret_id_file", a.SecretIDFile},
	} {
		if f.path == "" {
			continue
		}
		if b, err := os.ReadFile(f.path); err != nil {
			add("secrets.openbao.auth."+f.key, "%v", err)
		} else if strings.TrimSpace(string(b)) == "" {
			add("secrets.openbao.auth."+f.key, "%s is empty", f.path)
		}
	}
}

func (c Config) rejectOtherAuth(add func(key, format string, args ...any), method string, other bool) {
	if other {
		add("secrets.openbao.auth.method", "settings of another auth method are set; method %s uses its own only", method)
	}
}

// validKVPath checks a mount or prefix the way the backend does: 1 to 8 segments
// of [A-Za-z0-9][A-Za-z0-9._-]*, none containing "..".
func validKVPath(p string) bool {
	segs := strings.Split(p, "/")
	if p == "" || len(segs) > 8 {
		return false
	}
	for _, s := range segs {
		if s == "" || strings.Contains(s, "..") {
			return false
		}
		for i, r := range s {
			ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || (i > 0 && (r == '.' || r == '_' || r == '-'))
			if !ok {
				return false
			}
		}
	}
	return true
}
