package pluginsdk_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/invopop/jsonschema"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

type goldenInner struct {
	Host string `json:"host" jsonschema:"description=Target host"`
	Port *int   `json:"port,omitempty"`
}

type goldenIn struct {
	Name   string            `json:"name"`
	Inner  goldenInner       `json:"inner"`
	Ptr    *goldenInner      `json:"ptr,omitempty"`
	Tags   []string          `json:"tags,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
	Nested []goldenInner     `json:"nested,omitempty"`
}

// goldenChecksum pins the manifest checksum of the golden schema. If it
// changes, the generated schema (or the invopop/jsonschema version) changed,
// which agents and controllers see as a schema conflict.
const goldenChecksum = "4f4bc0f6efaee1008084810a2b1dae2c448135298fc897fae4b51663cc68f30b"

func goldenManifest() pluginsdk.Manifest {
	return pluginsdk.Manifest{Name: "golden", Version: "1.0.0", Title: "Golden",
		InputSchema: pluginsdk.MustSchemaFor[goldenIn](), OutputSchema: pluginsdk.MustSchemaFor[goldenInner](),
		TargetMode: pluginsdk.TargetNone}
}

func TestSchemaGolden(t *testing.T) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(pluginsdk.MustSchemaFor[goldenIn]()); err != nil {
		t.Fatal(err)
	}
	const path = "testdata/schema_golden.json"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("schema changed (UPDATE_GOLDEN=1 to accept):\n%s", buf.Bytes())
	}
	sum, err := goldenManifest().Checksum()
	if err != nil {
		t.Fatal(err)
	}
	if sum != goldenChecksum {
		t.Fatalf("checksum = %s, want %s", sum, goldenChecksum)
	}
}

func TestChecksumIgnoresKeyOrder(t *testing.T) {
	build := func(order []string) *jsonschema.Schema {
		props := jsonschema.NewProperties()
		for _, k := range order {
			props.Set(k, &jsonschema.Schema{Type: "string"})
		}
		return &jsonschema.Schema{Type: "object", Properties: props,
			Extras: map[string]any{"x-b": 1, "x-a": 2}}
	}
	mk := func(order []string) string {
		m := goldenManifest()
		m.InputSchema = build(order)
		sum, err := m.Checksum()
		if err != nil {
			t.Fatal(err)
		}
		return sum
	}
	a, b := mk([]string{"a", "b", "c"}), mk([]string{"c", "a", "b"})
	if a != b {
		t.Fatalf("key order changed checksum: %s vs %s", a, b)
	}
	if other := mk([]string{"a"}); other == a {
		t.Fatal("different schemas must differ")
	}
}
