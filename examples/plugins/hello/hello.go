// Package hello is a minimal Damson plugin that greets someone. It is the
// reference for out-of-tree plugins: it uses only pkg/pluginsdk, exports
// Plugins(), has no init() and no package-level mutable state.
package hello

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Input is the plugin input. Schemas are generated from these tags.
type Input struct {
	Name string `json:"name,omitempty" jsonschema:"description=Who to greet,default=world"`
}

// Output is the plugin output.
type Output struct {
	Greeting string `json:"greeting" jsonschema:"description=The greeting that was logged"`
}

// Plugins returns the plugins in this package. The manifest, with its
// generated schemas, is built once here.
func Plugins() []pluginsdk.Plugin {
	return []pluginsdk.Plugin{plugin{manifest: pluginsdk.Manifest{
		Name:         "hello",
		Version:      "0.1.0",
		Title:        "Hello",
		Description:  "Logs a greeting and returns it as output.",
		InputSchema:  pluginsdk.MustSchemaFor[Input](),
		OutputSchema: pluginsdk.MustSchemaFor[Output](),
		Idempotent:   true,
		TargetMode:   pluginsdk.TargetNone,
	}}}
}

type plugin struct{ manifest pluginsdk.Manifest }

func (p plugin) Manifest() pluginsdk.Manifest { return p.manifest }

func decode(raw json.RawMessage) (Input, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, pluginsdk.ValidationError("invalid input: %v", err)
	}
	if strings.ContainsAny(in.Name, "\r\n") {
		return in, pluginsdk.ValidationError("name must be a single line")
	}
	if in.Name == "" {
		in.Name = "world"
	}
	return in, nil
}

func (plugin) Validate(_ context.Context, input json.RawMessage) error {
	_, err := decode(input)
	return err
}

func (plugin) Execute(ctx context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
	if err := ctx.Err(); err != nil {
		return pluginsdk.Result{Error: pluginsdk.AsStepError(err)}, nil
	}
	in, err := decode(req.Input)
	if err != nil {
		return pluginsdk.Result{Error: pluginsdk.AsStepError(err)}, nil
	}
	greeting := fmt.Sprintf("Hello, %s!", in.Name)
	emit.Log(pluginsdk.Stdout, greeting)
	out, err := json.Marshal(Output{Greeting: greeting})
	if err != nil {
		return pluginsdk.Result{}, fmt.Errorf("encoding output: %w", err)
	}
	return pluginsdk.Result{Outputs: out}, nil
}
