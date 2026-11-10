package advance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/store"
)

func manifestRow(name, version string, idempotent bool) store.PluginManifest {
	raw, _ := json.Marshal(map[string]any{"name": name, "version": version, "idempotent": idempotent})
	return store.PluginManifest{Name: name, Version: version, Checksum: name + version, Manifest: raw}
}

func lostState(stepID string) engine.RunState {
	return engine.RunState{Steps: []engine.StepInstanceState{{
		Path: stepID, StepID: stepID, Status: engine.StepPending,
		Executions: []engine.ExecutionView{{ID: "e1", Attempt: 1, State: engine.ExecFailed}, {ID: "e2", Attempt: 2, State: engine.ExecLost}},
	}}}
}

func TestFillIdempotent(t *testing.T) {
	job := &engine.Job{Spec: engine.Spec{Steps: []engine.Step{{ID: "a", Plugin: "ssh"}, {ID: "b", Plugin: "tool@2"}}}}
	tests := []struct {
		name      string
		manifests []store.PluginManifest
		step      string
		ref       string
		want      bool
	}{
		{"idempotent manifest", []store.PluginManifest{manifestRow("ssh", "1.0.0", true)}, "a", "ssh", true},
		{"non-idempotent manifest", []store.PluginManifest{manifestRow("ssh", "1.0.0", false)}, "a", "ssh", false},
		{"no manifest", nil, "a", "ssh", false},
		{"every version must be idempotent", []store.PluginManifest{manifestRow("ssh", "1.0.0", true), manifestRow("ssh", "1.1.0", false)}, "a", "ssh", false},
		{"pinned major ignores other majors", []store.PluginManifest{manifestRow("tool", "2.3.0", true), manifestRow("tool", "1.0.0", false)}, "b", "tool@2", true},
		{"pinned major with no match", []store.PluginManifest{manifestRow("tool", "1.0.0", true)}, "b", "tool@2", false},
		{"another plugin does not count", []store.PluginManifest{manifestRow("other", "1.0.0", true)}, "a", "ssh", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &core{st: &fakeStore{manifests: tc.manifests}}
			state := lostState(tc.step)
			if err := c.fillIdempotent(context.Background(), &state, job); err != nil {
				t.Fatal(err)
			}
			if got := state.IdempotentPlugins[tc.ref]; got != tc.want {
				t.Fatalf("IdempotentPlugins[%q] = %v; want %v (map %v)", tc.ref, got, tc.want, state.IdempotentPlugins)
			}
		})
	}
}

// The manifests are read only when a step's latest attempt was lost.
func TestFillIdempotentOnlyForLostAttempts(t *testing.T) {
	job := &engine.Job{Spec: engine.Spec{Steps: []engine.Step{{ID: "a", Plugin: "ssh"}}}}
	failed := engine.RunState{Steps: []engine.StepInstanceState{{
		Path: "a", StepID: "a", Status: engine.StepPending,
		Executions: []engine.ExecutionView{{ID: "e1", Attempt: 1, State: engine.ExecLost}, {ID: "e2", Attempt: 2, State: engine.ExecFailed}},
	}}}
	c := &core{st: &fakeStore{manifests: []store.PluginManifest{manifestRow("ssh", "1.0.0", true)}}}
	if err := c.fillIdempotent(context.Background(), &failed, job); err != nil {
		t.Fatal(err)
	}
	if failed.IdempotentPlugins != nil {
		t.Fatalf("IdempotentPlugins = %v; want none when the latest attempt was not lost", failed.IdempotentPlugins)
	}
}
