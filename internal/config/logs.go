package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Log store names (logs.store).
const (
	LogStoreDB = "db"
	LogStoreS3 = "s3"
)

// Logs configures where log chunk objects are stored (ARCHITECTURE 10,
// ADR-0067).
type Logs struct {
	Store string `koanf:"store"` // db or s3
	S3    S3Logs `koanf:"s3"`
}

// S3Logs configures the s3 log store. Credentials are never logged: see Redacted.
type S3Logs struct {
	Endpoint        string `koanf:"endpoint"` // http(s) URL; empty is AWS
	Region          string `koanf:"region"`
	Bucket          string `koanf:"bucket"`
	Prefix          string `koanf:"prefix"`
	PathStyle       bool   `koanf:"path_style"`
	AllowPlainHTTP  bool   `koanf:"allow_plain_http"`
	TLSCAFile       string `koanf:"tls_ca_file"`
	AccessKeyID     string `koanf:"access_key_id"`
	SecretAccessKey string `koanf:"secret_access_key"`
	// SecretAccessKeyFile is read in place of secret_access_key.
	SecretAccessKeyFile string `koanf:"secret_access_key_file"`
	SessionToken        string `koanf:"session_token"`
	SSE                 string `koanf:"sse"` // "", AES256 or aws:kms
	SSEKMSKeyID         string `koanf:"sse_kms_key_id"`
}

// S3Configured reports whether chunks may be read from s3: a bucket is set.
func (c Config) S3Configured() bool { return c.Logs.S3.Bucket != "" }

// ResolveLogsS3Secret returns the secret access key: the value, else the
// content of the file with surrounding whitespace removed.
func (c Config) ResolveLogsS3Secret() (string, error) {
	s := c.Logs.S3
	if s.SecretAccessKey != "" || s.SecretAccessKeyFile == "" {
		return s.SecretAccessKey, nil
	}
	b, err := os.ReadFile(s.SecretAccessKeyFile)
	if err != nil {
		return "", fmt.Errorf("logs.s3.secret_access_key_file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// HostSecure splits logs.s3.endpoint into host[:port] and whether it is https.
// An empty endpoint is AWS.
func (s S3Logs) HostSecure() (host string, secure bool) {
	if s.Endpoint == "" {
		return "s3.amazonaws.com", true
	}
	u, _ := url.Parse(s.Endpoint)
	return u.Host, u.Scheme == "https"
}

// NormalizedPrefix returns the key prefix, empty or ending in "/".
func (s S3Logs) NormalizedPrefix() string {
	if s.Prefix == "" {
		return ""
	}
	return s.Prefix + "/"
}

func (c Config) validateLogs(add func(key, format string, args ...any)) {
	l := c.Logs
	switch l.Store {
	case LogStoreDB, LogStoreS3:
	case "fs":
		add("logs.store", "fs is not implemented; use db or s3")
	default:
		add("logs.store", "must be db or s3; got %q", l.Store)
	}
	s := l.S3
	if l.Store != LogStoreS3 && s.Bucket == "" {
		return // s3 is not in use
	}
	if s.Bucket == "" {
		add("logs.s3.bucket", "is required with logs.store = s3")
	}
	if s.Endpoint != "" {
		u, err := url.Parse(s.Endpoint)
		switch {
		case err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https"):
			add("logs.s3.endpoint", "must be an http(s) URL without credentials")
		case u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
			add("logs.s3.endpoint", "must not carry credentials, a path, a query or a fragment")
		case u.Scheme == "http" && !s.AllowPlainHTTP:
			add("logs.s3.endpoint", "is plain http; use https, or set logs.s3.allow_plain_http for a trusted network")
		}
	}
	if p := s.Prefix; p != "" {
		if strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
			add("logs.s3.prefix", "must not start or end with /; got %q", p)
		}
		for _, seg := range strings.Split(p, "/") {
			if seg == "" || seg == "." || seg == ".." {
				add("logs.s3.prefix", "must not hold empty, . or .. segments; got %q", p)
				break
			}
		}
	}
	if s.AccessKeyID != "" {
		switch {
		case s.SecretAccessKey == "" && s.SecretAccessKeyFile == "":
			add("logs.s3.secret_access_key", "is required with logs.s3.access_key_id (or set logs.s3.secret_access_key_file)")
		case s.SecretAccessKey != "" && s.SecretAccessKeyFile != "":
			add("logs.s3.secret_access_key", "and logs.s3.secret_access_key_file are both set; use one")
		default:
			if _, err := c.ResolveLogsS3Secret(); err != nil {
				add("logs.s3.secret_access_key_file", "%v", err)
			}
		}
	} else if s.SecretAccessKey != "" || s.SecretAccessKeyFile != "" || s.SessionToken != "" {
		add("logs.s3.access_key_id", "is required when a secret key or session token is set")
	}
	switch s.SSE {
	case "", "AES256":
		if s.SSEKMSKeyID != "" {
			add("logs.s3.sse_kms_key_id", "needs logs.s3.sse = aws:kms")
		}
	case "aws:kms":
	default:
		add("logs.s3.sse", "must be empty, AES256 or aws:kms; got %q", s.SSE)
	}
	if s.TLSCAFile != "" {
		if _, err := os.Stat(s.TLSCAFile); err != nil {
			add("logs.s3.tls_ca_file", "%v", err)
		}
	}
}
