package pluginreg

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

type cacheIn struct {
	Host string `json:"host"`
}

type cacheOut struct {
	OK bool `json:"ok"`
}

func TestSchemaCacheIsBounded(t *testing.T) {
	r, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.maxEntryCap = 4
	for i := range 50 {
		m := pluginsdk.Manifest{
			Name: fmt.Sprintf("p%d", i), Version: "1.0.0", Title: "t",
			InputSchema: pluginsdk.MustSchemaFor[cacheIn](), OutputSchema: pluginsdk.MustSchemaFor[cacheOut](),
			TargetMode: pluginsdk.TargetNone,
		}
		if err := r.ValidateInput(m, json.RawMessage(`{"host":"x"}`)); err != nil {
			t.Fatal(err)
		}
		r.vmu.Lock()
		n := len(r.validators)
		r.vmu.Unlock()
		if n > 4 {
			t.Fatalf("cache grew to %d", n)
		}
	}
}

func TestFailedCompilesAreNotCached(t *testing.T) {
	r, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := pluginsdk.Manifest{
		Name: "bad", Version: "1.0.0", Title: "t",
		InputSchema: pluginsdk.MustSchemaFor[cacheIn](), OutputSchema: pluginsdk.MustSchemaFor[cacheOut](),
		TargetMode: pluginsdk.TargetNone,
	}
	m.InputSchema.Ref = "file:///nope"
	for range 10 {
		if err := r.ValidateInput(m, json.RawMessage(`{}`)); err == nil {
			t.Fatal("expected a compile error")
		}
	}
	if len(r.validators) != 0 {
		t.Errorf("negative entries cached: %d", len(r.validators))
	}
}
