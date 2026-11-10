package advance

import (
	"testing"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/store"
)

func TestPoolFor(t *testing.T) {
	job := &engine.Job{Spec: engine.Spec{Steps: []engine.Step{
		{ID: "plain", Plugin: "ssh"},
		{ID: "pinned", Plugin: "ssh", RunsOn: &engine.RunsOn{Pool: "dc1"}},
		{ID: "labels", Plugin: "ssh", RunsOn: &engine.RunsOn{Labels: map[string]string{"a": "b"}}},
	}}}
	for path, want := range map[string]string{
		"plain":   store.PoolByNode, // dispatch picks the pool of the node
		"pinned":  "dc1",            // runs_on.pool wins over the node
		"labels":  store.PoolByNode, // labels alone do not choose a pool
		"missing": store.PoolByNode,
	} {
		if got := poolFor(job, path); got != want {
			t.Errorf("poolFor(%q) = %q, want %q", path, got, want)
		}
	}
}
