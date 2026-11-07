package protocol

import (
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// NodeInfo.Tags (SDK 0.4.0) is optional on the wire: it round-trips, is not
// encoded when empty, and a peer at N-1 without the field skips it.
func TestNodeInfoTagsAreBackwardCompatible(t *testing.T) {
	b, err := Encode(pluginsdk.NodeInfo{Name: "n", Address: "a", Tags: []string{"web", "prod"}})
	if err != nil {
		t.Fatal(err)
	}
	var got pluginsdk.NodeInfo
	if err := Decode(b, &got); err != nil || len(got.Tags) != 2 || got.Tags[0] != "web" {
		t.Fatalf("round trip = %+v, %v", got, err)
	}

	type oldNodeInfo struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	}
	var old oldNodeInfo
	if err := Decode(b, &old); err != nil || old.Name != "n" || old.Address != "a" {
		t.Fatalf("N-1 decode = %+v, %v", old, err)
	}

	b, _ = Encode(pluginsdk.NodeInfo{Name: "n", Address: "a"})
	var m map[string]any
	if err := Decode(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["tags"]; ok {
		t.Fatalf("tags encoded when empty: %v", m)
	}
}
