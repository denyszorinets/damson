package devnodes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
"nodes":{"sshd":{"address":"127.0.0.1","port":2222,"host_key_policy":"strict","host_key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl","credential":"secret://_inventory/sshd","tags":["dev"],"attributes":{"role":"web"}}},
"secrets":{"secret://_inventory/sshd":{"username":"damson","private_key_file":"` + keyPath + `"},
"secret://default/token":{"type":"token","token_file":"` + keyPath + `"}}}`
}

func TestLoadAndDesired(t *testing.T) {
	dir := t.TempDir()
	key := write(t, dir, "key", marker, 0o600)
	f, err := Load(write(t, dir, "nodes.json", fileJSON(key), 0o600))
	if err != nil {
		t.Fatal(err)
	}
	ds := f.Desired()
	if len(ds) != 1 {
		t.Fatalf("Desired = %+v", ds)
	}
	n := ds[0]
	if n.Name != "sshd" || n.ExternalID != "sshd" || n.Source != Source || n.Address != "127.0.0.1" || n.Port != 2222 ||
		n.Transport != "ssh" || !n.Enabled || n.Credential != "secret://_inventory/sshd" || n.Tags[0] != "dev" || n.Attributes["role"] != "web" {
		t.Fatalf("node = %+v", n)
	}
	if !f.HasSecrets() || len(f.secrets) != 2 {
		t.Fatalf("secrets = %+v", f.secrets)
	}
	// The default port follows the transport; a credential need not be defined in the file.
	f, err = Load(write(t, dir, "win.json", `{"version":1,"nodes":{"w":{"address":"10.0.0.9","transport":"winrm","host_key_policy":"strict","credential":"secret://default/elsewhere"}}}`, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if w := f.Desired()[0]; w.Port != 5986 || w.Transport != "winrm" {
		t.Fatalf("winrm node = %+v", w)
	}
	// Desired returns a copy.
	f.Desired()[0].Name = "changed"
	if f.Desired()[0].Name != "w" {
		t.Fatal("Desired shares its slice with the file")
	}
}

func TestLoadRejects(t *testing.T) {
	dir := t.TempDir()
	node := `"nodes":{"n":{"address":"10.0.0.1","host_key_policy":"strict"}}`
	tests := map[string]struct{ json, want string }{
		"unknown field":        {`{"version":1,"unknown":true}`, "unknown"},
		"wrong version":        {`{"version":2}`, "version 2"},
		"empty address":        {`{"version":1,"nodes":{"n":{"host_key_policy":"strict"}}}`, "address"},
		"address with port":    {`{"version":1,"nodes":{"n":{"address":"h:22","host_key_policy":"strict"}}}`, "address"},
		"bad policy":           {`{"version":1,"nodes":{"n":{"address":"h","host_key_policy":"lax"}}}`, "host_key_policy"},
		"missing policy":       {`{"version":1,"nodes":{"n":{"address":"h"}}}`, "host_key_policy"},
		"bad node name":        {`{"version":1,"nodes":{"a/b":{"address":"h","host_key_policy":"strict"}}}`, "name"},
		"case-only duplicates": {`{"version":1,"nodes":{"a":{"address":"h","host_key_policy":"strict"},"A":{"address":"h","host_key_policy":"strict"}}}`, "differ only in case"},
		"credential not a ref": {`{"version":1,"nodes":{"n":{"address":"h","host_key_policy":"strict","credential":"hunter2"}}}`, "credential"},
		"secret ref in attrs":  {`{"version":1,"nodes":{"n":{"address":"h","host_key_policy":"strict","attributes":{"a":"secret://x/y"}}}}`, "attributes"},
		"ref with field":       {`{"version":1,` + node + `,"secrets":{"secret://default/a#f":{"username":"u"}}}`, "without #field"},
		"underscore project":   {`{"version":1,` + node + `,"secrets":{"secret://_x/a":{"username":"u"}}}`, "scope"},
		"old style no scope":   {`{"version":1,` + node + `,"secrets":{"secret://a":{"username":"u"}}}`, "scope"},
		"inline value":         {`{"version":1,` + node + `,"secrets":{"secret://default/a":{"password":"hunter2"}}}`, `"password_file"`},
		"relative path":        {`{"version":1,` + node + `,"secrets":{"secret://default/a":{"password_file":"rel"}}}`, "absolute"},
		"unknown type":         {`{"version":1,` + node + `,"secrets":{"secret://default/a":{"type":"nope","username":"u"}}}`, "type"},
		"missing required":     {`{"version":1,` + node + `,"secrets":{"secret://default/a":{"type":"token","username":"u"}}}`, "token"},
		"no fields":            {`{"version":1,` + node + `,"secrets":{"secret://default/a":{"type":"generic"}}}`, "no fields"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, dir, "bad.json", tc.json, 0o600))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want one mentioning %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("error echoes a value: %v", err)
			}
		})
	}
	if _, err := Load(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("a missing file loaded")
	}
}

func TestSecretFileChecks(t *testing.T) {
	dir := t.TempDir()
	good := write(t, dir, "good", marker, 0o600)
	if b, err := readSecretFile(good); err != nil || string(b) != marker {
		t.Fatalf("good file: %q, %v", b, err)
	}
	for name, path := range map[string]string{
		"group readable": write(t, dir, "open", marker, 0o640),
		"directory":      dir,
		"missing":        filepath.Join(dir, "nope"),
		"too large":      write(t, dir, "big", strings.Repeat("x", maxKeyBytes+1), 0o600),
	} {
		_, err := readSecretFile(path)
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), marker) {
			t.Errorf("%s: error echoes the content: %v", name, err)
		}
	}
}
