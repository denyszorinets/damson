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
