package hello_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/denyszorinets/damson/examples/plugins/hello"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
)

func TestContract(t *testing.T) {
	ps := hello.Plugins()
	if len(ps) != 1 {
		t.Fatalf("Plugins() = %d plugins, want 1", len(ps))
	}
	plugintest.RunContract(t, ps[0], plugintest.Options{Cases: []plugintest.Case{
		{Name: "default", Input: json.RawMessage(`{}`)},
		{Name: "named", Input: json.RawMessage(`{"name":"damson"}`)},
	}})
}

func TestSource(t *testing.T) {
	plugintest.CheckSource(t, ".")
}

func TestExecute(t *testing.T) {
	tests := []struct {
		name, input, wantGreeting string
	}{
		{"default", `{}`, "Hello, world!"},
		{"named", `{"name":"damson"}`, "Hello, damson!"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := plugintest.Exec(context.Background(), t, hello.Plugins()[0], plugintest.Case{Input: json.RawMessage(tc.input)})
			if o.Err != nil || o.Result.Error != nil {
				t.Fatalf("unexpected failure: %v %v", o.Err, o.Result.Error)
			}
			var out hello.Output
			if err := json.Unmarshal(o.Result.Outputs, &out); err != nil || out.Greeting != tc.wantGreeting {
				t.Fatalf("outputs = %s (%v)", o.Result.Outputs, err)
			}
			if lines := o.Emitter.Lines(); len(lines) != 1 || lines[0].Line != tc.wantGreeting {
				t.Fatalf("lines = %v", lines)
			}
		})
	}
}
