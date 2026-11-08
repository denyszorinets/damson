package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidateLogs(t *testing.T) {
	base := func(f func(*Config)) Config {
		c := Config{Logs: Logs{Store: LogStoreS3, S3: S3Logs{Bucket: "b"}}}
		f(&c)
		return c
	}
	tests := []struct {
		name string
		c    Config
		want string // substring of the first error key; empty is valid
	}{
		{"db default", Config{Logs: Logs{Store: LogStoreDB}}, ""},
		{"fs refused", Config{Logs: Logs{Store: "fs"}}, "logs.store"},
		{"unknown store", Config{Logs: Logs{Store: "gcs"}}, "logs.store"},
		{"s3 minimal", base(func(*Config) {}), ""},
		{"s3 without bucket", Config{Logs: Logs{Store: LogStoreS3}}, "logs.s3.bucket"},
		{"plain http refused", base(func(c *Config) { c.Logs.S3.Endpoint = "http://minio:9000" }), "logs.s3.endpoint"},
		{"plain http allowed", base(func(c *Config) { c.Logs.S3.Endpoint = "http://minio:9000"; c.Logs.S3.AllowPlainHTTP = true }), ""},
		{"endpoint with path", base(func(c *Config) { c.Logs.S3.Endpoint = "https://s3.example.com/x" }), "logs.s3.endpoint"},
		{"endpoint with userinfo", base(func(c *Config) { c.Logs.S3.Endpoint = "https://u:p@s3.example.com" }), "logs.s3.endpoint"},
		{"prefix leading slash", base(func(c *Config) { c.Logs.S3.Prefix = "/x" }), "logs.s3.prefix"},
		{"prefix dotdot", base(func(c *Config) { c.Logs.S3.Prefix = "a/../b" }), "logs.s3.prefix"},
		{"prefix ok", base(func(c *Config) { c.Logs.S3.Prefix = "damson/prod" }), ""},
		{"key id without secret", base(func(c *Config) { c.Logs.S3.AccessKeyID = "k" }), "logs.s3.secret_access_key"},
		{"secret without key id", base(func(c *Config) { c.Logs.S3.SecretAccessKey = "s" }), "logs.s3.access_key_id"},
		{"static keys", base(func(c *Config) { c.Logs.S3.AccessKeyID = "k"; c.Logs.S3.SecretAccessKey = "s" }), ""},
		{"bad sse", base(func(c *Config) { c.Logs.S3.SSE = "rot13" }), "logs.s3.sse"},
		{"kms key without kms", base(func(c *Config) { c.Logs.S3.SSEKMSKeyID = "k" }), "logs.s3.sse_kms_key_id"},
		{"read-only s3 (db store, bucket set)", Config{Logs: Logs{Store: LogStoreDB, S3: S3Logs{Bucket: "b", Endpoint: "http://x"}}}, "logs.s3.endpoint"},
	}
	t.Run("a bad endpoint never echoes credentials", func(t *testing.T) {
		c := base(func(c *Config) { c.Logs.S3.Endpoint = "s3://AKIAKEY:hunter2@host" })
		var msg string
		c.validateLogs(func(_, format string, args ...any) { msg += fmt.Sprintf(format, args...) })
		if msg == "" || strings.Contains(msg, "hunter2") || strings.Contains(msg, "AKIAKEY") {
			t.Fatalf("message = %q", msg)
		}
	})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			tc.c.validateLogs(func(key, _ string, _ ...any) { got = append(got, key) })
			switch {
			case tc.want == "" && len(got) != 0:
				t.Fatalf("errors %v, want none", got)
			case tc.want != "" && (len(got) == 0 || !strings.Contains(got[0], tc.want)):
				t.Fatalf("errors %v, want %q", got, tc.want)
			}
		})
	}
}
