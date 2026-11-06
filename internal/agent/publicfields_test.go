package agent

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func s3Bundle(extra map[string]string) map[string]map[string]string {
	f := map[string]string{
		"access_key": "AKIAEXAMPLEKEY1234", "secret_key": "s3cr3t/KEY+value9876", "session_token": "tok-SESSION-abcdef",
		"endpoint": "minio:9000", "region": "us-east-1", "path_style": "true",
	}
	for k, v := range extra {
		f[k] = v
	}
	return map[string]map[string]string{"secret://s3/conn": f}
}

func TestS3PublicFieldsStayVisibleAndRealSecretsMasked(t *testing.T) {
	lm := NewLocalMasking(s3Bundle(nil))
	line := "backups/us-east-1/x true minio:9000"
	if got := lm.Mask(line); got != line {
		t.Fatalf("public fields masked: %q", got)
	}
	for _, secret := range []string{"AKIAEXAMPLEKEY1234", "s3cr3t/KEY+value9876", "tok-SESSION-abcdef"} {
		for _, form := range []string{secret, base64.StdEncoding.EncodeToString([]byte(secret))} {
			if got := lm.Mask("x " + form + " y"); strings.Contains(got, form) {
				t.Errorf("%q leaked: %q", form, got)
			}
		}
	}
	raw, _ := json.Marshal(map[string]string{"destination_key": "backups/us-east-1/x", "k": "s3cr3t/KEY+value9876"})
	out, redacted, err := lm.Outputs(raw)
	if err != nil || out["destination_key"] != "backups/us-east-1/x" || out["k"] != "***" || len(redacted) != 1 {
		t.Fatalf("outputs = %v %v %v", out, redacted, err)
	}
	if lm.ShortSecrets() {
		t.Error("path_style=true must not count as a short secret")
	}
}

func TestPublicFieldGuardsFailSafe(t *testing.T) {
	tests := []struct {
		name   string
		extra  map[string]string
		masked string // value that must be masked
	}{
		{"userinfo", map[string]string{"endpoint": "https://u:pw0rd@host"}, "https://u:pw0rd@host"},
		{"path", map[string]string{"endpoint": "https://host/path"}, "https://host/path"},
		{"query", map[string]string{"endpoint": "host?x=yyyy"}, "host?x=yyyy"},
		{"fragment", map[string]string{"endpoint": "host#frag"}, "host#frag"},
		{"space", map[string]string{"endpoint": " host.example"}, " host.example"},
		{"upper scheme", map[string]string{"endpoint": "HTTPS://host.example"}, "HTTPS://host.example"},
		{"webhook", map[string]string{"endpoint": "hooks.slack.com/services/T/B/X"}, "hooks.slack.com/services/T/B/X"},
		{"bad region", map[string]string{"region": "Abc_DEF"}, "Abc_DEF"},
		{"long region", map[string]string{"region": strings.Repeat("a", 33)}, strings.Repeat("a", 33)},
		{"bad path_style", map[string]string{"path_style": "maybe"}, "maybe"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lm := NewLocalMasking(s3Bundle(tc.extra))
			if got := lm.Mask("a " + tc.masked + " b"); strings.Contains(got, tc.masked) {
				t.Fatalf("not masked: %q", got)
			}
		})
	}
}

func TestPublicFieldsNeedTheS3Shape(t *testing.T) {
	lm := NewLocalMasking(map[string]map[string]string{"secret://x": {"endpoint": "internal.example", "token": "abcdefgh"}})
	if got := lm.Mask("internal.example"); got == "internal.example" {
		t.Fatal("endpoint of a non-S3 secret must be masked")
	}
}

func TestPublicFieldsUnionWithRealSecrets(t *testing.T) {
	// A secret equal to a public value elsewhere is still masked.
	b := s3Bundle(nil)
	b["secret://other"] = map[string]string{"password": "us-east-1"}
	if got := NewLocalMasking(b).Mask("region us-east-1"); strings.Contains(got, "us-east-1") {
		t.Fatalf("got %q", got)
	}
	b = s3Bundle(map[string]string{"secret_key": "us-east-1"})
	if got := NewLocalMasking(b).Mask("region us-east-1"); strings.Contains(got, "us-east-1") {
		t.Fatalf("got %q", got)
	}
}
