package devnodes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
)

const marker = "PRIVATE-KEY-MARKER"

func write(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func fileJSON(keyPath string) string {
	return `{"version":1,
"nodes":{"sshd":{"address":"127.0.0.1","port":2222,"host_key_policy":"strict","host_key":"ssh-ed25519 AAAA","credential":"secret://dev/sshd"}},
"secrets":{"secret://dev/sshd":{"username":"damson","private_key_file":"` + keyPath + `"}}}`
}

func TestLoadAndResolve(t *testing.T) {
	dir := t.TempDir()
	key := write(t, dir, "key", marker, 0o600)
	f, err := Load(write(t, dir, "nodes.json", fileJSON(key), 0o600))
	if err != nil {
		t.Fatal(err)
	}
	n, err := f.Node(context.Background(), "r", "sshd")
	if err != nil || n.Address != "127.0.0.1" || n.Port != 2222 || n.Transport != "ssh" || n.Name != "sshd" || n.Credential != "secret://dev/sshd" {
		t.Fatalf("node = %+v, %v", n, err)
	}
	if _, err := f.Node(context.Background(), "r", "nope"); !errors.Is(err, dispatch.ErrUnknownNode) {
		t.Fatalf("unknown node err = %v", err)
	}
	got, err := f.Secret(context.Background(), "r", "secret://dev/sshd")
	if err != nil || got["private_key"] != marker || got["username"] != "damson" {
		t.Fatalf("secret = %v, %v", got, err)
	}
	// Nothing is cached: a rewritten key is served at the next call.
	write(t, dir, "key", "rotated", 0o600)
	if got, _ := f.Secret(context.Background(), "r", "secret://dev/sshd"); got["private_key"] != "rotated" {
		t.Fatalf("key after rotation = %q", got["private_key"])
	}
	if _, err := f.Secret(context.Background(), "r", "secret://dev/other"); !errors.Is(err, dispatch.ErrUnknownSecret) {
		t.Fatalf("unknown secret err = %v", err)
	}
	if names := f.Names(); len(names) != 1 || names[0] != "sshd" {
		t.Fatalf("names = %v", names)
	}
}

func TestLoadRejects(t *testing.T) {
	dir := t.TempDir()
	key := write(t, dir, "key", marker, 0o600)
	tests := []struct{ name, body string }{
		{"unknown field", `{"version":1,"nodes":{},"secrets":{},"extra":1}`},
		{"bad version", `{"version":2}`},
		{"inline private key", `{"version":1,"secrets":{"secret://a":{"private_key":"` + marker + `"}}}`},
		{"relative path", `{"version":1,"secrets":{"secret://a":{"private_key_file":"key"}}}`},
		{"ref with field", `{"version":1,"secrets":{"secret://a#f":{"username":"x"}}}`},
		{"not a ref", `{"version":1,"secrets":{"a":{"username":"x"}}}`},
		{"undefined credential", `{"version":1,"nodes":{"n":{"address":"h","host_key_policy":"strict","credential":"secret://zz"}}}`},
		{"bad policy", `{"version":1,"nodes":{"n":{"address":"h","host_key_policy":"yolo"}}}`},
		{"no address", `{"version":1,"nodes":{"n":{"host_key_policy":"strict"}}}`},
		{"bad port", `{"version":1,"nodes":{"n":{"address":"h","port":70000,"host_key_policy":"strict"}}}`},
		{"secret ref in attributes", `{"version":1,"nodes":{"n":{"address":"h","host_key_policy":"strict","attributes":{"a":{"b":["secret://x"]}}}}}`},
		{"not json", `nope`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(write(t, dir, "bad.json", tc.body, 0o600))
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), marker) {
				t.Fatalf("error leaks a value: %v", err)
			}
		})
	}
	if _, err := Load(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing file accepted")
	}
	_ = key
}

func TestSecretFileChecks(t *testing.T) {
	dir := t.TempDir()
	open := write(t, dir, "open", marker, 0o644)
	big := write(t, dir, "big", strings.Repeat("x", maxKeyBytes+1), 0o600)
	for name, path := range map[string]string{"group/world readable": open, "too large": big, "missing": filepath.Join(dir, "gone"), "directory": dir} {
		t.Run(name, func(t *testing.T) {
			f, err := Load(write(t, dir, "n.json", `{"version":1,"secrets":{"secret://a":{"value_file":"`+path+`"}}}`, 0o600))
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.Secret(context.Background(), "r", "secret://a")
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), marker) {
				t.Fatalf("error leaks file content: %v", err)
			}
		})
	}
}

func TestTargetsAreSortedAndCarryNoSecrets(t *testing.T) {
	dir := t.TempDir()
	key := write(t, dir, "key", marker, 0o600)
	content := `{"version":1,
"nodes":{
 "web-02":{"address":"10.0.0.2","os":"linux","host_key_policy":"insecure","attributes":{"role":"web","rack":3}},
 "db-01":{"address":"10.0.0.9","port":5022,"transport":"ssh","host_key_policy":"strict","host_key":"ssh-ed25519 AAAA","credential":"secret://dev/sshd"},
 "web-01":{"address":"10.0.0.1","os":"linux","host_key_policy":"insecure","attributes":{"role":"web"}}},
"secrets":{"secret://dev/sshd":{"username":"damson","private_key_file":"` + key + `"}}}`
	f, err := Load(write(t, dir, "nodes.json", content, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	targets := f.Targets()
	var names []string
	for _, n := range targets {
		names = append(names, n["name"].(string))
	}
	if strings.Join(names, ",") != "db-01,web-01,web-02" {
		t.Fatalf("targets = %v; want them sorted by name", names)
	}
	web := targets[2]
	if web["address"] != "10.0.0.2" || web["os"] != "linux" || web["transport"] != "ssh" || web["pool"] != "" {
		t.Fatalf("web-02 = %v", web)
	}
	if attrs := web["attributes"].(map[string]any); attrs["role"] != "web" || attrs["rack"] != float64(3) {
		t.Fatalf("attributes = %v", web["attributes"])
	}
	if tags, ok := web["tags"].([]any); !ok || len(tags) != 0 {
		t.Fatalf("tags = %#v; want an empty list", web["tags"])
	}
	// The expression context never gets the credential, the host key or the policy.
	dump := fmt.Sprint(targets)
	for _, forbidden := range []string{"secret://", "AAAA", "credential", "host_key", marker, "strict", "insecure"} {
		if strings.Contains(dump, forbidden) {
			t.Fatalf("targets leak %q: %s", forbidden, dump)
		}
	}
	// A copy: changing it does not change the file's nodes.
	targets[2]["attributes"].(map[string]any)["role"] = "changed"
	if again := f.Targets(); again[2]["attributes"].(map[string]any)["role"] != "web" {
		t.Fatal("Targets shares the node attributes with the file")
	}
}
