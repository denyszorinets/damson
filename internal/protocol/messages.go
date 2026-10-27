package protocol

import (
	"encoding/json"
	"fmt"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// WireManifest carries a pluginsdk.Manifest. The manifest holds JSON Schema
// documents whose Go types only define JSON encoding, so on the wire it is
// converted through generic values and written as a plain MessagePack map.
type WireManifest struct {
	pluginsdk.Manifest
}

// EncodeMsgpack implements msgpack.CustomEncoder.
func (m WireManifest) EncodeMsgpack(enc *msgpack.Encoder) error {
	raw, err := json.Marshal(m.Manifest)
	if err != nil {
		return fmt.Errorf("encoding manifest %q: %w", m.Name, err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return fmt.Errorf("encoding manifest %q: %w", m.Name, err)
	}
	return enc.Encode(generic)
}

// DecodeMsgpack implements msgpack.CustomDecoder.
func (m *WireManifest) DecodeMsgpack(dec *msgpack.Decoder) error {
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return err
	}
	raw, err := json.Marshal(generic)
	if err != nil {
		return fmt.Errorf("decoding manifest: %w", err)
	}
	if err := json.Unmarshal(raw, &m.Manifest); err != nil {
		return fmt.Errorf("decoding manifest: %w", err)
	}
	return nil
}

// Execution states reported in agent.hello.
const (
	ExecRunning  = "running"  // still executing
	ExecFinished = "finished" // finished, step.complete not yet acknowledged
)

// ExecutionState is one execution the agent still knows about.
type ExecutionState struct {
	AttemptID string `msgpack:"attempt_id" json:"attempt_id"`
	// State is ExecRunning or ExecFinished.
	State string `msgpack:"state" json:"state"`
}

// HelloRequest is the params of agent.hello (A->C).
type HelloRequest struct {
	Protocol int `msgpack:"protocol" json:"protocol"`
	// SessionSeq is a per-agent-process connection counter, starting at 1 and
	// incremented on every connection attempt. The controller orders competing
	// hellos by it, not by wall clocks.
	SessionSeq    uint64            `msgpack:"session_seq" json:"session_seq"`
	AgentVersion  string            `msgpack:"agent_version" json:"agent_version"`
	Pool          string            `msgpack:"pool" json:"pool"`
	Labels        map[string]string `msgpack:"labels,omitempty" json:"labels,omitempty"`
	MaxConcurrent int               `msgpack:"max_concurrent" json:"max_concurrent"`
	Capabilities  []string          `msgpack:"capabilities,omitempty" json:"capabilities,omitempty"`
	Manifests     []WireManifest    `msgpack:"manifests,omitempty" json:"manifests,omitempty"`
	// Executions lists running attempts and finished attempts whose
	// step.complete was not acknowledged.
	Executions []ExecutionState `msgpack:"executions,omitempty" json:"executions,omitempty"`
}

// Reconciliation actions in the hello response.
const (
	ActionContinue = "continue"
	ActionCancel   = "cancel"
)

// Reconciliation is the controller's decision for one attempt in hello.
type Reconciliation struct {
	AttemptID string `msgpack:"attempt_id" json:"attempt_id"`
	// Action is ActionContinue or ActionCancel.
	Action string `msgpack:"action" json:"action"`
}

// Limits are the controller-imposed limits sent in the hello response.
type Limits struct {
	MaxFrameBytes      int `msgpack:"max_frame_bytes" json:"max_frame_bytes"`
	LogBatchBytes      int `msgpack:"log_batch_bytes" json:"log_batch_bytes"`
	LogBatchIntervalMS int `msgpack:"log_batch_interval_ms" json:"log_batch_interval_ms"`
}

// HelloResponse is the result of agent.hello (C->A).
type HelloResponse struct {
	// Protocol is the accepted protocol version (see Negotiate).
	Protocol            int              `msgpack:"protocol" json:"protocol"`
	HeartbeatIntervalMS int              `msgpack:"heartbeat_interval_ms" json:"heartbeat_interval_ms"`
	Limits              Limits           `msgpack:"limits" json:"limits"`
	Reconciliation      []Reconciliation `msgpack:"reconciliation,omitempty" json:"reconciliation,omitempty"`
}

// Heartbeat is the params of agent.heartbeat (A->C, notification).
type Heartbeat struct {
	RunningAttempts []string `msgpack:"running_attempts" json:"running_attempts"`
	FreeSlots       int      `msgpack:"free_slots" json:"free_slots"`
	CPUPercent      float64  `msgpack:"cpu_percent" json:"cpu_percent"`
	MemoryBytes     uint64   `msgpack:"memory_bytes" json:"memory_bytes"`
	Load1           float64  `msgpack:"load1" json:"load1"`
	SpoolBytes      int64    `msgpack:"spool_bytes" json:"spool_bytes"`
}

// TraceContext carries W3C trace context headers.
type TraceContext struct {
	Traceparent string `msgpack:"traceparent,omitempty" json:"traceparent,omitempty"`
	Tracestate  string `msgpack:"tracestate,omitempty" json:"tracestate,omitempty"`
}

// StepExecute is the params of step.execute (C->A).
type StepExecute struct {
	AttemptID     string `msgpack:"attempt_id" json:"attempt_id"`
	ExecutionID   string `msgpack:"execution_id,omitempty" json:"execution_id,omitempty"`
	StepPath      string `msgpack:"step_path" json:"step_path"`
	PluginName    string `msgpack:"plugin" json:"plugin"`
	PluginVersion string `msgpack:"plugin_version" json:"plugin_version"`
	// Input is the resolved plugin input object; secrets stay as refs.
	Input map[string]any `msgpack:"input,omitempty" json:"input,omitempty"`
	// Secrets holds the values of every ref used by the step. Never log it.
	Secrets SecretBundle                  `msgpack:"secrets,omitempty" json:"secrets,omitempty"`
	Nodes   map[string]pluginsdk.NodeInfo `msgpack:"nodes,omitempty" json:"nodes,omitempty"`
	// TimeoutMS is zero for no timeout.
	TimeoutMS int64        `msgpack:"timeout_ms,omitempty" json:"timeout_ms,omitempty"`
	Trace     TraceContext `msgpack:"trace" json:"trace"`
}

// StepExecuteResult is the response to step.execute: accepted or rejected.
type StepExecuteResult struct {
	Accepted bool `msgpack:"accepted" json:"accepted"`
	// Reason is set when Accepted is false, for example "no free slot",
	// "unknown plugin" or "draining".
	Reason string `msgpack:"reason,omitempty" json:"reason,omitempty"`
}

// LogEntry is one log line. On the wire it is the array [ts, stream, line]
// where ts is Unix nanoseconds.
type LogEntry struct {
	_msgpack struct{}         `msgpack:",as_array"` //nolint:unused // msgpack as_array marker
	TS       int64            `msgpack:"ts" json:"ts"`
	Stream   pluginsdk.Stream `msgpack:"stream" json:"stream"`
	Line     string           `msgpack:"line" json:"line"`
}

// MarshalJSON writes the [ts, stream, line] array form.
func (e LogEntry) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{e.TS, e.Stream, e.Line})
}

// UnmarshalJSON reads the [ts, stream, line] array form.
func (e *LogEntry) UnmarshalJSON(b []byte) error {
	var a [3]json.RawMessage
	if err := json.Unmarshal(b, &a); err != nil {
		return fmt.Errorf("decoding log entry: %w", err)
	}
	if err := json.Unmarshal(a[0], &e.TS); err != nil {
		return fmt.Errorf("decoding log entry ts: %w", err)
	}
	if err := json.Unmarshal(a[1], &e.Stream); err != nil {
		return fmt.Errorf("decoding log entry stream: %w", err)
	}
	if err := json.Unmarshal(a[2], &e.Line); err != nil {
		return fmt.Errorf("decoding log entry line: %w", err)
	}
	return nil
}

// LogAppend is the params of log.append (A->C, notification). Entry i has
// sequence number SeqFrom+i.
type LogAppend struct {
	AttemptID string     `msgpack:"attempt_id" json:"attempt_id"`
	SeqFrom   uint64     `msgpack:"seq_from" json:"seq_from"`
	Entries   []LogEntry `msgpack:"entries" json:"entries"`
}

// LogAck is the params of log.ack (C->A, notification): the highest
// contiguous sequence number persisted.
type LogAck struct {
	AttemptID string `msgpack:"attempt_id" json:"attempt_id"`
	Seq       uint64 `msgpack:"seq" json:"seq"`
}

// StepProgress is the params of step.progress (A->C, notification).
type StepProgress struct {
	AttemptID string `msgpack:"attempt_id" json:"attempt_id"`
	// Percent is 0 to 100.
	Percent float64 `msgpack:"percent" json:"percent"`
	Message string  `msgpack:"message,omitempty" json:"message,omitempty"`
}

// Step completion statuses.
const (
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCanceled  = "canceled"
	StatusTimedOut  = "timed_out"
)

// StepError describes a failed step.
type StepError struct {
	Code      string `msgpack:"code,omitempty" json:"code,omitempty"`
	Message   string `msgpack:"message" json:"message"`
	Retryable bool   `msgpack:"retryable,omitempty" json:"retryable,omitempty"`
}

// StepComplete is the params of step.complete (A->C). It is retried until
// acknowledged, including across reconnects.
type StepComplete struct {
	AttemptID string         `msgpack:"attempt_id" json:"attempt_id"`
	Status    string         `msgpack:"status" json:"status"`
	Outputs   map[string]any `msgpack:"outputs,omitempty" json:"outputs,omitempty"`
	Error     *StepError     `msgpack:"error,omitempty" json:"error,omitempty"`
	// StartedAtNS and FinishedAtNS are Unix nanoseconds.
	StartedAtNS  int64 `msgpack:"started_at_ns" json:"started_at_ns"`
	FinishedAtNS int64 `msgpack:"finished_at_ns" json:"finished_at_ns"`
}

// StepCompleteAck is the result of step.complete. Stale is true when the
// controller discarded the result because the attempt was no longer current;
// the agent must stop retrying either way.
type StepCompleteAck struct {
	Acknowledged bool `msgpack:"acknowledged" json:"acknowledged"`
	Stale        bool `msgpack:"stale,omitempty" json:"stale,omitempty"`
}

// StepCancel is the params of step.cancel (C->A).
type StepCancel struct {
	AttemptID string `msgpack:"attempt_id" json:"attempt_id"`
	GraceMS   int64  `msgpack:"grace_ms" json:"grace_ms"`
}

// StepCancelResult is the result of step.cancel. Known is false when the
// agent has no such running attempt.
type StepCancelResult struct {
	Known bool `msgpack:"known" json:"known"`
}

// Drain is the params of agent.drain (C->A, notification).
type Drain struct {
	Reason string `msgpack:"reason,omitempty" json:"reason,omitempty"`
}

// Reconnect is the params of agent.reconnect (C->A, notification). The agent
// reconnects through the load balancer after a random delay in [0, JitterMS].
type Reconnect struct {
	JitterMS int64  `msgpack:"jitter_ms" json:"jitter_ms"`
	Reason   string `msgpack:"reason,omitempty" json:"reason,omitempty"`
}

// HostKeyObserved is the params of node.hostkey_observed (A->C,
// notification): a TOFU host key report. Applied only if AttemptID is current.
type HostKeyObserved struct {
	AttemptID string `msgpack:"attempt_id" json:"attempt_id"`
	Node      string `msgpack:"node" json:"node"`
	KeyType   string `msgpack:"key_type" json:"key_type"`
	// HostKey is the public key in authorized_keys format.
	HostKey string `msgpack:"host_key" json:"host_key"`
	// Fingerprint is SHA256:<base64>.
	Fingerprint string `msgpack:"fingerprint" json:"fingerprint"`
}
