package engine_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/engine"
)

func TestWithOptionDefaults(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"env":   map[string]any{"type": "string", "default": "prod"},
			"count": map[string]any{"type": "integer"},
			"tags":  map[string]any{"type": "array", "default": []any{"a"}},
			"nested": map[string]any{
				"type":       "object",
				"properties": map[string]any{"level": map[string]any{"default": "info"}},
			},
		},
	}
	tests := []struct {
		name   string
		schema map[string]any
		in     map[string]any
		want   map[string]any
	}{
		{name: "fills missing", schema: schema, in: nil,
			want: map[string]any{"env": "prod", "tags": []any{"a"}}},
		{name: "keeps given values", schema: schema, in: map[string]any{"env": "dev", "count": float64(3)},
			want: map[string]any{"env": "dev", "count": float64(3), "tags": []any{"a"}}},
		{name: "recurses into given objects", schema: schema, in: map[string]any{"nested": map[string]any{}},
			want: map[string]any{"env": "prod", "tags": []any{"a"}, "nested": map[string]any{"level": "info"}}},
		{name: "no schema", schema: nil, in: map[string]any{"x": "y"}, want: map[string]any{"x": "y"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := &engine.Job{Spec: engine.Spec{Options: tt.schema}}
			got, err := engine.WithOptionDefaults(job, tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWithOptionDefaults_DoesNotAliasSchemaOrInput(t *testing.T) {
	job := &engine.Job{Spec: engine.Spec{Options: map[string]any{
		"properties": map[string]any{"tags": map[string]any{"default": []any{"a"}}},
	}}}
	in := map[string]any{}
	got, err := engine.WithOptionDefaults(job, in)
	require.NoError(t, err)
	got["tags"].([]any)[0] = "mutated"
	assert.Equal(t, []any{"a"}, job.Spec.Options["properties"].(map[string]any)["tags"].(map[string]any)["default"])
	assert.Empty(t, in)
}
