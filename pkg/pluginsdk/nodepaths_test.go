package pluginsdk_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

func nodesManifest(paths ...string) pluginsdk.Manifest {
	return pluginsdk.Manifest{
		Name: "x", Version: "1.0.0", Title: "x", TargetMode: pluginsdk.TargetNodes, NodePaths: paths,
		InputSchema: pluginsdk.MustSchemaFor[struct{}](), OutputSchema: pluginsdk.MustSchemaFor[struct{}](),
	}
}

func TestManifestNodePathsValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		m    pluginsdk.Manifest
		want string // substring of the error; empty means valid
	}{
		"valid":         {nodesManifest("/source/node/name", "/destination/node/name"), ""},
		"none":          {nodesManifest(), "1 to 8"},
		"too many":      {nodesManifest("/a", "/b", "/c", "/d", "/e", "/f", "/g", "/h", "/i"), "1 to 8"},
		"root pointer":  {nodesManifest(""), "JSON Pointer"},
		"no slash":      {nodesManifest("source"), "JSON Pointer"},
		"empty segment": {nodesManifest("/a//b"), "JSON Pointer"},
		"bad escape":    {nodesManifest("/a~2"), "JSON Pointer"},
		"duplicate":     {nodesManifest("/a", "/a"), "unique"},
		"paths w/o mode": {func() pluginsdk.Manifest {
			m := nodesManifest("/a")
			m.TargetMode = pluginsdk.TargetSingleNode
			return m
		}(), "needs target mode"},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.m.Validate()
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("Validate() = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestNodeNames(t *testing.T) {
	m := nodesManifest("/source/node/name", "/destination/node/name")
	single := pluginsdk.Manifest{TargetMode: pluginsdk.TargetSingleNode}
	for name, tc := range map[string]struct {
		m     pluginsdk.Manifest
		input string
		want  []string
		err   string
	}{
		"both":         {m, `{"source":{"node":{"name":"a"}},"destination":{"node":{"name":"b"}}}`, []string{"a", "b"}, ""},
		"same node":    {m, `{"source":{"node":{"name":"a"}},"destination":{"node":{"name":"a"}}}`, []string{"a"}, ""},
		"one is s3":    {m, `{"source":{"s3":{"bucket":"b"}},"destination":{"node":{"name":"b"}}}`, []string{"b"}, ""},
		"neither":      {m, `{"source":{"s3":{}},"destination":{"s3":{}}}`, nil, ""},
		"non-string":   {m, `{"source":{"node":{"name":5}}}`, nil, "/source/node/name"},
		"empty":        {m, `{"source":{"node":{"name":""}}}`, nil, "/source/node/name"},
		"through list": {m, `{"source":["x"]}`, nil, ""},
		"bad pointer":  {nodesManifest("source"), `{"source":"a"}`, nil, ""},
		"empty ptr":    {nodesManifest(""), `{"source":"a"}`, nil, ""},
		"single":       {single, `{"node":"n1"}`, []string{"n1"}, ""},
		"single none":  {single, `{}`, nil, `"node"`},
		"single bad":   {single, `{"node":3}`, nil, "/node"},
		"none mode":    {pluginsdk.Manifest{TargetMode: pluginsdk.TargetNone}, `{"node":"n1"}`, nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			in, err := decodeObject(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			got, err := tc.m.NodeNames(in)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v; want %q", err, tc.err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("NodeNames = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

// node_paths must not change the checksum of a manifest that has none.
func TestChecksumIgnoresEmptyNodePaths(t *testing.T) {
	m := nodesManifest()
	m.TargetMode = pluginsdk.TargetNone
	raw, err := jsonMarshal(m)
	if err != nil || strings.Contains(string(raw), "node_paths") {
		t.Fatalf("marshaled manifest = %s, %v", raw, err)
	}
}

func decodeObject(s string) (map[string]any, error) {
	var v map[string]any
	return v, json.Unmarshal([]byte(s), &v)
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
