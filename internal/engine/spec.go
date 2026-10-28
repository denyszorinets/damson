// Package engine is the pure heart of Damson: the job spec model and
// validation (ARCHITECTURE §5), and Decide, which turns a run's state into
// actions (§8.2).
//
// The package performs no I/O, never reads the clock (callers pass now) and
// uses no randomness. All control-flow semantics of §5.3 live here.
package engine

// Spec constants.
const (
	APIVersion = "damson.dev/v1"
	KindJob    = "Job"

	// StepTypePlugin is the implicit step type: a plugin invocation.
	StepTypePlugin = "plugin"
	// StepTypeGroup is reserved for M3 and rejected by validation today.
	StepTypeGroup = "group"
)

// Job is a complete job document (ARCHITECTURE §5.1). It is the unit that is
// stored as an immutable JobVersion.
type Job struct {
	APIVersion string   `json:"apiVersion" jsonschema:"const=damson.dev/v1"`
	Kind       string   `json:"kind" jsonschema:"const=Job"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`
}

// Metadata identifies the job.
type Metadata struct {
	Project     string `json:"project" jsonschema:"minLength=1"`
	Name        string `json:"name" jsonschema:"minLength=1"`
	Description string `json:"description,omitempty"`
}

// Spec is the executable part of a job.
type Spec struct {
	// Options is a JSON Schema (an object schema) describing the run inputs.
	Options map[string]any `json:"options,omitempty"`
	// Targets selects nodes from the inventory. Parsed and checked now;
	// evaluated by the controller from M2's inventory work.
	Targets *Targets `json:"targets,omitempty"`
	// Concurrency is reserved for M3; validation rejects it today.
	Concurrency *Concurrency `json:"concurrency,omitempty"`
	// Timeout covers the whole run, as a Go duration such as "30m".
	Timeout string `json:"timeout,omitempty"`
	// Steps run sequentially (§5.3 rule 1).
	Steps []Step `json:"steps" jsonschema:"minItems=1"`
}

// Targets is the node selection of a job (§5.5).
type Targets struct {
	// Selector is a CEL expression over `node` and `inputs`.
	Selector string `json:"selector,omitempty"`
}

// Concurrency limits parallel runs of a job (reserved, M3).
type Concurrency struct {
	Key    string `json:"key,omitempty"`
	Max    int    `json:"max,omitempty"`
	Policy string `json:"policy,omitempty"`
}

// Step is one entry of a step list (§5.2). Fields marked reserved are parsed
// so that M3 extends this type instead of reshaping it, but validation
// rejects them with a "not supported yet" error until their milestone.
type Step struct {
	// ID is unique within its enclosing scope and matches [a-z0-9-]+.
	ID string `json:"id"`
	// Type is "plugin" (default) or "group" (reserved).
	Type string `json:"type,omitempty" jsonschema:"enum=plugin,enum=group"`
	// Plugin is "name" or "name@<major>".
	Plugin string `json:"plugin,omitempty"`
	// With is the plugin input. Strings may contain ${{ }} expressions; the
	// resolved result is validated against the plugin's input schema.
	With map[string]any `json:"with,omitempty"`
	// Timeout is per attempt, as a Go duration.
	Timeout string `json:"timeout,omitempty"`
	// When is a CEL condition. Default: success().
	When string `json:"when,omitempty"`
	// ContinueOnError records a failure without failing the scope.
	ContinueOnError bool `json:"continue_on_error,omitempty"`
	// RunsOn selects the agent pool and labels.
	RunsOn *RunsOn `json:"runs_on,omitempty"`

	// Reserved for M3 (groups, catch/finally, for_each, retries).
	Steps         []Step `json:"steps,omitempty"`
	Catch         []Step `json:"catch,omitempty"`
	Finally       []Step `json:"finally,omitempty"`
	Rethrow       *bool  `json:"rethrow,omitempty"`
	ForEach       string `json:"for_each,omitempty"`
	MaxParallel   *int   `json:"max_parallel,omitempty"`
	FailThreshold *int   `json:"fail_threshold,omitempty"`
	Retry         *Retry `json:"retry,omitempty"`
}

// RunsOn is the optional agent selection of a step.
type RunsOn struct {
	Pool   string            `json:"pool,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// Retry configures retries of a step (reserved, M3).
type Retry struct {
	MaxAttempts int      `json:"max_attempts,omitempty"`
	Backoff     *Backoff `json:"backoff,omitempty"`
	RetryOn     string   `json:"retry_on,omitempty"`
	OnLost      bool     `json:"on_lost,omitempty"`
}

// Backoff is the retry delay curve (reserved, M3).
type Backoff struct {
	Initial    string  `json:"initial,omitempty"`
	Multiplier float64 `json:"multiplier,omitempty"`
	Max        string  `json:"max,omitempty"`
	Jitter     float64 `json:"jitter,omitempty"`
}

// IsGroup reports whether the step is a group (reserved).
func (s Step) IsGroup() bool { return s.Type == StepTypeGroup }
