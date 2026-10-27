package pluginsdk

import (
	"context"
	"encoding/json"
	"log/slog"
)

// Plugin is implemented by every Damson plugin.
type Plugin interface {
	// Manifest describes the plugin. It must be cheap and side-effect free.
	Manifest() Manifest
	// Validate runs on the agent before Execute (cross-field rules). A failure
	// is a non-retryable validation error. The controller validates only
	// against Manifest().InputSchema, because it may not have the plugin's code.
	Validate(ctx context.Context, input json.RawMessage) error
	// Execute runs on the agent. It must honor ctx cancellation and kill any
	// remote processes it started where possible.
	//
	// A step failure is reported in Result.Error. A non-nil error return means
	// the plugin itself failed unexpectedly; callers map it with AsStepError.
	Execute(ctx context.Context, req ExecRequest, emit Emitter) (Result, error)
}

// ExecRequest carries everything a plugin needs for one execution.
type ExecRequest struct {
	// Input is the fully resolved input; secret references are still refs.
	Input json.RawMessage
	// Secrets resolves secret:// refs from the bundle sent with the request.
	Secrets SecretResolver
	// Nodes are the nodes referenced by this step, keyed by node name.
	Nodes map[string]NodeInfo
	// WorkDir is a per-execution temp dir, removed after Execute returns.
	WorkDir string
	// Logger is for diagnostics only. It never receives secret values.
	Logger *slog.Logger
}

// SecretResolver resolves secret:// references to their values. Values must
// only be held in memory and never logged or returned in outputs.
type SecretResolver interface {
	Resolve(ref string) (string, error)
}

// NodeInfo is the connection detail of one inventory node.
type NodeInfo struct {
	Name       string            `json:"name"`
	Address    string            `json:"address"`
	Port       int               `json:"port,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
	// Credential is a secret:// ref, never a value.
	Credential string `json:"credential,omitempty"`
}

// Stream identifies the log stream a line belongs to.
type Stream string

// Log streams.
const (
	Stdout Stream = "stdout"
	Stderr Stream = "stderr"
	System Stream = "system"
)

// Emitter streams logs and progress to the run log. Implementations mask
// secret values before anything leaves the agent (ARCHITECTURE.md section 9.4).
type Emitter interface {
	Log(stream Stream, line string)
	Progress(pct float64, msg string)
}

// Result is the outcome of one Execute call.
type Result struct {
	// Outputs is a JSON object conforming to the manifest's OutputSchema.
	Outputs json.RawMessage `json:"outputs,omitempty"`
	// Error is non-nil when the step failed.
	Error *StepError `json:"error,omitempty"`
}
