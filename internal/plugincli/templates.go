package plugincli

const goModTmpl = `module {{.Module}}

go {{.GoVersion}}

// Run "go get github.com/denyszorinets/damson@<version>" and "go mod tidy" to
// add the Damson requirement.
`

const pluginTmpl = `// Package {{.Pkg}} is the {{.Name}} plugin for Damson. It uses only
// github.com/denyszorinets/damson/pkg/pluginsdk, exports Plugins(), has no
// init() and no package-level mutable state, so it can be linked into a custom
// binary with "damson build --with".
package {{.Pkg}}

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Input is the plugin input. The input schema is generated from these tags.
type Input struct {
	Message string ` + "`" + `json:"message,omitempty" jsonschema:"description=Text to echo,default=hello"` + "`" + `
}

// Output is the plugin output. The output schema is generated from these tags.
type Output struct {
	Echo string ` + "`" + `json:"echo" jsonschema:"description=The text that was logged"` + "`" + `
}

// Plugins returns the plugins in this package.
func Plugins() []pluginsdk.Plugin {
	return []pluginsdk.Plugin{plugin{manifest: pluginsdk.Manifest{
		Name:         "{{.Name}}",
		Version:      "0.1.0",
		Title:        "{{.Title}}",
		Description:  "Echoes a message to the step log.",
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
	if strings.ContainsAny(in.Message, "\r\n") {
		return in, pluginsdk.ValidationError("message must be a single line")
	}
	if in.Message == "" {
		in.Message = "hello"
	}
	return in, nil
}

// Validate runs on the agent before Execute: cross-field rules the schema
// cannot express.
func (plugin) Validate(_ context.Context, input json.RawMessage) error {
	_, err := decode(input)
	return err
}

// Execute runs the step. It must honor ctx and never log or return secrets.
func (plugin) Execute(ctx context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
	if err := ctx.Err(); err != nil {
		return pluginsdk.Result{Error: pluginsdk.AsStepError(err)}, nil
	}
	in, err := decode(req.Input)
	if err != nil {
		return pluginsdk.Result{Error: pluginsdk.AsStepError(err)}, nil
	}
	emit.Log(pluginsdk.Stdout, in.Message)
	out, err := json.Marshal(Output{Echo: in.Message})
	if err != nil {
		return pluginsdk.Result{}, fmt.Errorf("encoding output: %w", err)
	}
	return pluginsdk.Result{Outputs: out}, nil
}
`

const testTmpl = `package {{.Pkg}}_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"

	{{.Pkg}} "{{.Module}}"
)

// TestContract runs the suite every Damson plugin must pass: manifest, schemas,
// validation, cancellation, secret masking and output conformance.
func TestContract(t *testing.T) {
	plugintest.RunContract(t, {{.Pkg}}.Plugins()[0], plugintest.Options{Cases: []plugintest.Case{
		{Name: "default", Input: json.RawMessage(` + "`{}`" + `)},
		{Name: "message", Input: json.RawMessage(` + "`{\"message\":\"hi\"}`" + `)},
	}})
}

// TestSource checks the source rules for linkable plugins: no init(), no
// package-level mutable state, only the public SDK imported.
func TestSource(t *testing.T) { plugintest.CheckSource(t, ".") }

func TestExecute(t *testing.T) {
	o := plugintest.Exec(context.Background(), t, {{.Pkg}}.Plugins()[0], plugintest.Case{
		Input: json.RawMessage(` + "`{\"message\":\"hi\"}`" + `),
	})
	if o.Err != nil || o.Result.Error != nil {
		t.Fatalf("unexpected failure: %v %v", o.Err, o.Result.Error)
	}
	var out {{.Pkg}}.Output
	if err := json.Unmarshal(o.Result.Outputs, &out); err != nil || out.Echo != "hi" {
		t.Fatalf("outputs = %s (%v)", o.Result.Outputs, err)
	}
	if lines := o.Emitter.Lines(); len(lines) != 1 || lines[0].Line != "hi" {
		t.Fatalf("lines = %v", lines)
	}
}
`

const readmeTmpl = `# {{.Title}}

The {{.Name}} plugin for [Damson](https://github.com/denyszorinets/damson).

It echoes a message to the step log and returns it as the output ` + "`echo`" + `.

## Develop

` + "```" + `
go get github.com/denyszorinets/damson@<version>
go mod tidy
go test ./...
` + "```" + `

Edit ` + "`Input`" + ` and ` + "`Output`" + ` in ` + "`{{.Pkg}}.go`" + `: the JSON schemas are generated from their
tags. Keep ` + "`Plugins()`" + ` as the only entry point, and do not add ` + "`init()`" + ` or
package-level mutable state; ` + "`TestSource`" + ` checks that.

## Try it

Link the plugin into a custom Damson binary, then check and run it locally:

` + "```" + `
damson build --with {{.Module}}@<version> -o damson-custom
./damson-custom plugin validate {{.Name}}
echo '{"message":"hi"}' > input.json
./damson-custom plugin run {{.Name}} --input input.json
` + "```" + `

A step uses it as ` + "`\"plugin\": \"{{.Name}}\"`" + ` (or ` + "`{{.Name}}@0`" + ` to pin the major version).
`
