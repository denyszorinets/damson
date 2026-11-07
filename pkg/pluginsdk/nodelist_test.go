package pluginsdk_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

func TestInventoryNodeList(t *testing.T) {
	m := pluginsdk.Manifest{TargetMode: pluginsdk.TargetInventory, NodeListPath: "/hosts"}
	in := func(s string) map[string]any {
		var v map[string]any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	names, err := m.NodeNames(in(`{"hosts":["web2","web1","web2"]}`))
	if err != nil || !slices.Equal(names, []string{"web1", "web2"}) {
		t.Fatalf("names = %v, %v; want sorted and deduplicated", names, err)
	}
	if names, err = m.NodeNames(in(`{"hosts":[]}`)); err != nil || len(names) != 0 {
		t.Fatalf("empty list = %v, %v", names, err)
	}
	for _, bad := range []string{`{}`, `{"hosts":"a"}`, `{"hosts":{"a":1}}`, `{"hosts":["a",""]}`, `{"hosts":["a",1]}`} {
		if _, err := m.NodeNames(in(bad)); err == nil {
			t.Errorf("%s: want an error", bad)
		}
	}
	big := make([]any, pluginsdk.MaxNodeList+1)
	for i := range big {
		big[i] = "n"
	}
	if _, err := m.NodeNames(map[string]any{"hosts": big}); err == nil {
		t.Error("more than MaxNodeList names must fail")
	}
}

func TestNodeListPathValidation(t *testing.T) {
	base := func(mode pluginsdk.TargetMode) pluginsdk.Manifest {
		return pluginsdk.Manifest{
			Name: "x", Version: "1.0.0", Title: "x", TargetMode: mode,
			InputSchema: pluginsdk.MustSchemaFor[struct{}](), OutputSchema: pluginsdk.MustSchemaFor[struct{}](),
		}
	}
	ok := base(pluginsdk.TargetInventory)
	ok.NodeListPath = "/hosts"
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid inventory manifest: %v", err)
	}
	for name, m := range map[string]pluginsdk.Manifest{
		"inventory without path": base(pluginsdk.TargetInventory),
		"inventory bad pointer":  func() pluginsdk.Manifest { m := base(pluginsdk.TargetInventory); m.NodeListPath = "hosts"; return m }(),
		"inventory with paths": func() pluginsdk.Manifest {
			m := ok
			m.NodePaths = []string{"/node"}
			return m
		}(),
		"path on single_node": func() pluginsdk.Manifest { m := base(pluginsdk.TargetSingleNode); m.NodeListPath = "/hosts"; return m }(),
		"path on none":        func() pluginsdk.Manifest { m := base(pluginsdk.TargetNone); m.NodeListPath = "/hosts"; return m }(),
	} {
		if err := m.Validate(); err == nil {
			t.Errorf("%s: want a validation error", name)
		}
	}
}

// omitempty keeps the checksum of a manifest without the new field unchanged.
func TestNodeListPathDoesNotChangeOtherChecksums(t *testing.T) {
	m := pluginsdk.Manifest{Name: "x", Version: "1.0.0", Title: "x", TargetMode: pluginsdk.TargetNone,
		InputSchema: pluginsdk.MustSchemaFor[struct{}](), OutputSchema: pluginsdk.MustSchemaFor[struct{}]()}
	raw, _ := json.Marshal(m)
	if strings.Contains(string(raw), "node_list_path") || strings.Contains(string(raw), `"tags"`) {
		t.Fatalf("manifest JSON carries the new fields: %s", raw)
	}
}
