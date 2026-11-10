package pluginsdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

type in struct {
	Name  string `json:"name" jsonschema:"required,description=who"`
	Count int    `json:"count,omitempty" jsonschema:"minimum=1"`
}

func TestSchemaFor(t *testing.T) {
	s, err := pluginsdk.SchemaFor[in]()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	props, _ := m["properties"].(map[string]any)
	if props["name"] == nil || props["count"] == nil {
		t.Fatalf("missing properties: %s", b)
	}
	if req, _ := m["required"].([]any); len(req) != 1 || req[0] != "name" {
		t.Fatalf("required = %v", m["required"])
	}
	if _, err := pluginsdk.SchemaFor[int](); err == nil {
		t.Fatal("non-struct should fail")
	}
}

func TestManifestValidate(t *testing.T) {
	good := func() pluginsdk.Manifest {
		return pluginsdk.Manifest{
			Name: "ssh", Version: "1.2.3", Title: "SSH",
			InputSchema: pluginsdk.MustSchemaFor[in](), OutputSchema: pluginsdk.MustSchemaFor[in](),
			TargetMode: pluginsdk.TargetSingleNode, Requires: []string{"bin:git"},
		}
	}
	tests := []struct {
		name    string
		mutate  func(*pluginsdk.Manifest)
		wantErr bool
	}{
		{"ok", func(*pluginsdk.Manifest) {}, false},
		{"prerelease", func(m *pluginsdk.Manifest) { m.Version = "1.0.0-rc.1" }, false},
		{"bad name", func(m *pluginsdk.Manifest) { m.Name = "SSH!" }, true},
		{"empty name", func(m *pluginsdk.Manifest) { m.Name = "" }, true},
		{"bad version", func(m *pluginsdk.Manifest) { m.Version = "1.0" }, true},
		{"no title", func(m *pluginsdk.Manifest) { m.Title = "" }, true},
		{"nil input", func(m *pluginsdk.Manifest) { m.InputSchema = nil }, true},
		{"nil output", func(m *pluginsdk.Manifest) { m.OutputSchema = nil }, true},
		{"bad mode", func(m *pluginsdk.Manifest) { m.TargetMode = "x" }, true},
		{"bad require", func(m *pluginsdk.Manifest) { m.Requires = []string{"git"} }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := good()
			tc.mutate(&m)
			if err := m.Validate(); (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestChecksumStable(t *testing.T) {
	m := pluginsdk.Manifest{Name: "a", Version: "1.0.0", Title: "A",
		InputSchema: pluginsdk.MustSchemaFor[in](), OutputSchema: pluginsdk.MustSchemaFor[in](), TargetMode: pluginsdk.TargetNone}
	a, err := m.Checksum()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := m.Checksum()
	if a != b || len(a) != 64 {
		t.Fatalf("unstable checksum %q %q", a, b)
	}
	m.Version = "1.0.1"
	if c, _ := m.Checksum(); c == a {
		t.Fatal("checksum should change with manifest")
	}
}

func TestAsStepError(t *testing.T) {
	se := pluginsdk.ValidationError("bad %s", "x")
	tests := []struct {
		name string
		err  error
		want pluginsdk.ErrorKind
	}{
		{"step error", se, pluginsdk.KindValidation},
		{"wrapped", fmt.Errorf("ctx: %w", se), pluginsdk.KindValidation},
		{"canceled", fmt.Errorf("x: %w", context.Canceled), pluginsdk.KindCancelled},
		{"deadline", context.DeadlineExceeded, pluginsdk.KindTimeout},
		{"other", errors.New("boom"), pluginsdk.KindInternal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pluginsdk.AsStepError(tc.err); got.Kind != tc.want {
				t.Fatalf("kind = %s, want %s", got.Kind, tc.want)
			}
		})
	}
	if pluginsdk.AsStepError(nil) != nil {
		t.Fatal("nil should stay nil")
	}
	if e := pluginsdk.ExitCodeError(3, "x"); e.ExitCode == nil || *e.ExitCode != 3 || e.Kind != pluginsdk.KindExitCode {
		t.Fatalf("bad exit error %+v", e)
	}
	if !errors.Is(pluginsdk.NewError(pluginsdk.KindInternal, "m", context.Canceled), context.Canceled) {
		t.Fatal("cause not unwrapped")
	}
}

func TestEnumValid(t *testing.T) {
	for _, m := range []pluginsdk.TargetMode{pluginsdk.TargetNone, pluginsdk.TargetSingleNode, pluginsdk.TargetInventory} {
		if !m.Valid() {
			t.Errorf("mode %q should be valid", m)
		}
	}
	for _, m := range []pluginsdk.TargetMode{"", "None", "single", "inventory "} {
		if m.Valid() {
			t.Errorf("mode %q should be invalid", m)
		}
	}
	kinds := []pluginsdk.ErrorKind{pluginsdk.KindConnection, pluginsdk.KindAuth, pluginsdk.KindTimeout,
		pluginsdk.KindExitCode, pluginsdk.KindValidation, pluginsdk.KindCancelled, pluginsdk.KindInternal}
	for _, k := range kinds {
		if !k.Valid() {
			t.Errorf("kind %q should be valid", k)
		}
	}
	for _, k := range []pluginsdk.ErrorKind{"", "exit", "Auth"} {
		if k.Valid() {
			t.Errorf("kind %q should be invalid", k)
		}
	}
}

func TestResultJSONRoundTrip(t *testing.T) {
	r := pluginsdk.Result{
		Outputs: json.RawMessage(`{"a":1}`),
		Error:   pluginsdk.ExitCodeError(7, "failed"),
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"outputs":{"a":1},"error":{"kind":"exit_code","message":"failed","exit_code":7}}`
	if string(b) != want {
		t.Fatalf("wire = %s, want %s", b, want)
	}
	var got pluginsdk.Result
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Error == nil || got.Error.Kind != pluginsdk.KindExitCode || *got.Error.ExitCode != 7 || got.Error.Message != "failed" {
		t.Fatalf("round trip lost data: %+v", got.Error)
	}
	// The cause is never serialized.
	b, _ = json.Marshal(pluginsdk.NewError(pluginsdk.KindInternal, "m", errors.New("secret detail")))
	if string(b) != `{"kind":"internal","message":"m"}` {
		t.Fatalf("wire = %s", b)
	}
	b, _ = json.Marshal(pluginsdk.Result{})
	if string(b) != `{}` {
		t.Fatalf("empty result wire = %s", b)
	}
}

func TestNodeInfoWire(t *testing.T) {
	n := pluginsdk.NodeInfo{Name: "n1", Address: "10.0.0.1", Port: 22, Transport: "ssh", OS: "linux",
		Attributes: map[string]any{"rack": 4, "prod": true, "env": "x"}, Credential: "secret://_inventory/n1",
		HostKey: "ssh-ed25519 AAAA", HostKeyPolicy: "strict"}
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"name", "address", "port", "transport", "os", "attributes", "credential", "host_key", "host_key_policy"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing wire key %q in %s", k, b)
		}
	}
}
