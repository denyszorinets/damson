package engine_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/engine"
)

var secretSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"token": map[string]any{"type": "string", "x-damson-secret": true},
		"keys":  map[string]any{"type": "array", "x-damson-secret": true, "items": map[string]any{"type": "string"}},
		"db": map[string]any{"type": "object", "properties": map[string]any{
			"password": map[string]any{"type": "string", "x-damson-secret": true},
			"host":     map[string]any{"type": "string"},
		}},
		"plain": map[string]any{"type": "string"},
	},
}

func TestSecretOptionViolations(t *testing.T) {
	tests := []struct {
		name string
		opts map[string]any
		want []string
	}{
		{name: "refs only", opts: map[string]any{"token": "secret://v/x", "keys": []any{"secret://a", "secret://b"}, "plain": "hello"}},
		{name: "plaintext", opts: map[string]any{"token": "hunter2"}, want: []string{"options.token"}},
		{name: "bare prefix is not a ref", opts: map[string]any{"token": "secret://"}, want: []string{"options.token"}},
		{name: "non string", opts: map[string]any{"token": 5.0}, want: []string{"options.token"}},
		{name: "array element", opts: map[string]any{"keys": []any{"secret://a", "plain"}}, want: []string{"options.keys[1]"}},
		{name: "nested", opts: map[string]any{"db": map[string]any{"password": "pw", "host": "h"}}, want: []string{"options.db.password"}},
		{name: "unmarked is free", opts: map[string]any{"plain": "anything", "db": map[string]any{"host": "x"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, engine.SecretOptionViolations(secretSchema, tt.opts))
		})
	}
}

func TestValidate_SecretOptionSchema(t *testing.T) {
	doc := func(opts string) []byte {
		return fmt.Appendf(nil, `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"p","name":"j"},
			"spec":{"options":%s,"steps":[{"id":"a","plugin":"ssh"}]}}`, opts)
	}
	tests := []struct {
		name    string
		opts    string
		wantErr string
	}{
		{name: "ref default", opts: `{"type":"object","properties":{"t":{"type":"string","x-damson-secret":true,"default":"secret://v/x"}}}`},
		{name: "plaintext default", opts: `{"type":"object","properties":{"t":{"type":"string","x-damson-secret":true,"default":"pw"}}}`, wantErr: "spec.options.properties.t.default"},
		{name: "plaintext const", opts: `{"type":"object","properties":{"t":{"x-damson-secret":true,"const":"pw"}}}`, wantErr: "spec.options.properties.t.const"},
		{name: "plaintext enum", opts: `{"type":"object","properties":{"t":{"x-damson-secret":true,"enum":["secret://a","b"]}}}`, wantErr: "spec.options.properties.t.enum[1]"},
		{name: "integer secret", opts: `{"type":"object","properties":{"t":{"type":"integer","x-damson-secret":true}}}`, wantErr: "spec.options.properties.t.type"},
		{name: "child of secret array", opts: `{"type":"object","properties":{"t":{"type":"array","x-damson-secret":true,"items":{"type":"string","default":"pw"}}}}`, wantErr: "items.default"},
		{name: "unmarked default is fine", opts: `{"type":"object","properties":{"t":{"type":"string","default":"pw"}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := engine.Parse(doc(tt.opts))
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestSecretOptionKeys(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"plain":  map[string]any{"type": "string"},
			"key":    map[string]any{"type": "string", "x-damson-secret": true},
			"nested": map[string]any{"type": "object", "properties": map[string]any{"pw": map[string]any{"x-damson-secret": true}}},
			"list":   map[string]any{"type": "array", "items": map[string]any{"x-damson-secret": true}},
		},
		"additionalProperties": map[string]any{"x-damson-secret": true},
	}
	got := engine.SecretOptionKeys(schema, []string{"plain", "key", "nested", "list", "unlisted"})
	want := []string{"key", "list", "nested", "unlisted"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := engine.SecretOptionKeys(nil, []string{"a"}); len(got) != 0 {
		t.Errorf("nil schema: %v", got)
	}
}
