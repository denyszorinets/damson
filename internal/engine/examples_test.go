package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/expr"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/all"
)

// Every job under examples/jobs parses, and the input of each of its plugin
// steps, with the expressions resolved against sample inputs and step outputs,
// is accepted by that plugin's Validate. So an example cannot rot unnoticed.
func TestExampleJobsAreValid(t *testing.T) {
	files, err := filepath.Glob("../../examples/jobs/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no example jobs: %v", err)
	}
	plugins := map[string]pluginsdk.Plugin{}
	for _, p := range all.Plugins() {
		plugins[p.Manifest().Name] = p
	}
	env, err := expr.NewEnv(expr.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	vars := expr.Vars{
		Inputs: map[string]any{"ref": "main", "version": "1.4.0"},
		Steps: map[string]any{"build": map[string]any{
			"status": "succeeded",
			"outputs": map[string]any{
				"image": "registry.internal/team/app", "digest": "sha256:0123456789012345678901234567890123456789012345678901234567890123",
				"image_ref": "registry.internal/team/app@sha256:0123456789012345678901234567890123456789012345678901234567890123",
				"tags":      []any{"1.4.0"}, "commit": "0123456789012345678901234567890123456789",
			},
		}},
		Run: map[string]any{"id": "r1", "status": "running"}, Job: map[string]any{"name": "x", "project": "team/app"},
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			job, err := engine.Parse(raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			checked := 0
			var walk func(steps []engine.Step)
			walk = func(steps []engine.Step) {
				for _, s := range steps {
					if s.IsGroup() {
						walk(s.Steps)
						walk(s.Catch)
						walk(s.Finally)
						continue
					}
					p, ok := plugins[s.Plugin]
					if !ok {
						t.Errorf("step %s: no built-in plugin %q", s.ID, s.Plugin)
						continue
					}
					resolved, err := env.Resolve(s.With, vars)
					if err != nil {
						t.Errorf("step %s: resolving with: %v", s.ID, err)
						continue
					}
					checked++
					in, _ := json.Marshal(resolved)
					if err := p.Validate(context.Background(), in); err != nil {
						t.Errorf("step %s: %s rejects its input: %v\n%s", s.ID, s.Plugin, err, in)
					}
				}
			}
			walk(job.Spec.Steps)
			if checked < 4 {
				t.Errorf("only %d plugin steps were checked", checked)
			}
		})
	}
}
