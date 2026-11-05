package inventory

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/store"
)

const goodKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl comment"

func good() store.Node {
	return store.Node{
		Name: "web-1", Address: "web-1.example.com", Port: 22, Transport: TransportSSH, HostKeyPolicy: PolicyStrict,
		Source: SourceManual, Enabled: true,
	}
}

func fields(is []Issue) string {
	var out []string
	for _, i := range is {
		out = append(out, i.Field)
	}
	return strings.Join(out, ",")
}

func TestValidate(t *testing.T) {
	long := strings.Repeat("a", 64)
	cases := []struct {
		name string
		edit func(*store.Node)
		bad  string // expected failing field, "" for valid
	}{
		{"valid", func(*store.Node) {}, ""},
		{"ipv4", func(n *store.Node) { n.Address = "10.0.0.7" }, ""},
		{"ipv6", func(n *store.Node) { n.Address = "fe80::1" }, ""},
		{"credential in inventory", func(n *store.Node) { n.Credential = "secret://_inventory/web-key" }, ""},
		{"credential in a project", func(n *store.Node) { n.Credential = "secret://default/web-key" }, ""},
		{"host key", func(n *store.Node) { n.HostKey = goodKey }, ""},
		{"name too long", func(n *store.Node) { n.Name = long }, "name"},
		{"name with slash", func(n *store.Node) { n.Name = "a/b" }, "name"},
		{"name with bracket", func(n *store.Node) { n.Name = "a[0]" }, "name"},
		{"name starts with dot", func(n *store.Node) { n.Name = ".a" }, "name"},
		{"empty address", func(n *store.Node) { n.Address = "" }, "address"},
		{"address with port", func(n *store.Node) { n.Address = "host:22" }, "address"},
		{"address with scheme", func(n *store.Node) { n.Address = "ssh://host" }, "address"},
		{"address with user", func(n *store.Node) { n.Address = "root@host" }, "address"},
		{"address with space", func(n *store.Node) { n.Address = "a b" }, "address"},
		{"ipv6 zone", func(n *store.Node) { n.Address = "fe80::1%eth0" }, "address"},
		{"ipv6 brackets", func(n *store.Node) { n.Address = "[::1]" }, "address"},
		{"hostname label too long", func(n *store.Node) { n.Address = strings.Repeat("a", 64) + ".com" }, "address"},
		{"port 0", func(n *store.Node) { n.Port = 0 }, "port"},
		{"port too high", func(n *store.Node) { n.Port = 65536 }, "port"},
		{"transport", func(n *store.Node) { n.Transport = "telnet" }, "transport"},
		{"os uppercase", func(n *store.Node) { n.OS = "Linux" }, "os"},
		{"pool uppercase", func(n *store.Node) { n.Pool = "Prod" }, "pool"},
		{"bad tag", func(n *store.Node) { n.Tags = []string{"ok", "no spaces"} }, "tags"},
		{"too many tags", func(n *store.Node) {
			for i := range 65 {
				n.Tags = append(n.Tags, "t"+strings.Repeat("x", i%5)+string(rune('a'+i%26))+string(rune('a'+i/26)))
			}
		}, "tags"},
		{"attribute nested", func(n *store.Node) { n.Attributes = map[string]any{"a": map[string]any{"b": 1}} }, "attributes.a"},
		{"attribute null", func(n *store.Node) { n.Attributes = map[string]any{"a": nil} }, "attributes.a"},
		{"attribute array", func(n *store.Node) { n.Attributes = map[string]any{"a": []any{1}} }, "attributes.a"},
		{"attribute secret ref", func(n *store.Node) { n.Attributes = map[string]any{"a": "secret://p/x"} }, "attributes.a"},
		{"attribute bad key", func(n *store.Node) { n.Attributes = map[string]any{"a-b": 1} }, "attributes"},
		{"attribute long string", func(n *store.Node) { n.Attributes = map[string]any{"a": strings.Repeat("x", 2000)} }, "attributes.a"},
		{"credential not a ref", func(n *store.Node) { n.Credential = "hunter2" }, "credential"},
		{"credential with field", func(n *store.Node) { n.Credential = "secret://_inventory/k#password" }, "credential"},
		{"credential deep inventory", func(n *store.Node) { n.Credential = "secret://_inventory/a/b" }, "credential"},
		{"credential underscore project", func(n *store.Node) { n.Credential = "secret://_x/y" }, "credential"},
		{"two host keys", func(n *store.Node) { n.HostKey = goodKey + "\n" + goodKey }, "host_key"},
		{"junk host key", func(n *store.Node) { n.HostKey = "not a key" }, "host_key"},
		{"host key on winrm", func(n *store.Node) { n.Transport, n.Port, n.HostKey = TransportWinRM, 5986, goodKey }, "host_key"},
		{"policy", func(n *store.Node) { n.HostKeyPolicy = "lax" }, "host_key_policy"},
		{"insecure is storable", func(n *store.Node) { n.HostKeyPolicy = PolicyInsecure }, ""},
		{"no source", func(n *store.Node) { n.Source = "" }, "source"},
		{"source uppercase", func(n *store.Node) { n.Source = "Sync" }, "source"},
		{"source too long", func(n *store.Node) { n.Source = long }, "source"},
		{"external id on manual", func(n *store.Node) { n.ExternalID = "x" }, "external_id"},
		{"external id too long", func(n *store.Node) { n.Source, n.ExternalID = "sync1", strings.Repeat("x", 256) }, "external_id"},
		{"external id control char", func(n *store.Node) { n.Source, n.ExternalID = "sync1", "a\nb" }, "external_id"},
		{"external id ok", func(n *store.Node) { n.Source, n.ExternalID = "sync1", "i-0abc/def" }, ""},
		{"tag that looks like a ref", func(n *store.Node) { n.Tags = []string{"secret://prod/pw"} }, "tags"},
		{"huge integer", func(n *store.Node) { n.Attributes = map[string]any{"a": int64(1) << 60} }, "attributes.a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := good()
			c.edit(&n)
			is := Validate(&n)
			if c.bad == "" && len(is) > 0 {
				t.Fatalf("unexpected issues: %+v", is)
			}
			if c.bad != "" && !strings.Contains(","+fields(is)+",", ","+c.bad+",") {
				t.Fatalf("issues = %q, want one for %q", fields(is), c.bad)
			}
			for _, i := range is {
				if strings.Contains(i.Message, "hunter2") {
					t.Errorf("message echoes the value: %q", i.Message)
				}
			}
		})
	}
}

func TestValidateNormalizes(t *testing.T) {
	n := good()
	n.Tags = []string{"prod", "db", "prod"}
	n.HostKey = goodKey
	if is := Validate(&n); len(is) > 0 {
		t.Fatal(is)
	}
	if strings.Join(n.Tags, ",") != "db,prod" {
		t.Errorf("tags = %v", n.Tags)
	}
	if strings.Contains(n.HostKey, "comment") || !strings.HasPrefix(n.HostKey, "ssh-ed25519 ") {
		t.Errorf("host key = %q; want the key without its comment", n.HostKey)
	}
	if DefaultPort(TransportSSH) != 22 || DefaultPort(TransportWinRM) != 5986 {
		t.Error("default ports")
	}
}

func TestValidateAttributeNumbers(t *testing.T) {
	n := good()
	n.Attributes = map[string]any{"i": 3, "i64": int64(4), "u": uint32(5), "f": 1.5, "n": json.Number("7"), "s": "x", "b": true}
	if is := Validate(&n); len(is) > 0 {
		t.Fatal(is)
	}
	for k, want := range map[string]any{"i": 3.0, "i64": 4.0, "u": 5.0, "f": 1.5, "n": 7.0, "s": "x", "b": true} {
		if n.Attributes[k] != want {
			t.Errorf("%s = %#v (%T), want %#v", k, n.Attributes[k], n.Attributes[k], want)
		}
	}
	bad := good()
	bad.Attributes = map[string]any{"a": math.NaN()}
	if is := Validate(&bad); len(is) == 0 {
		t.Error("NaN accepted")
	}
}
