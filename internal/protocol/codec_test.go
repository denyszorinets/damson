package protocol

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/invopop/jsonschema"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

func sampleManifest() WireManifest {
	type in struct {
		Host string `json:"host"`
	}
	r := &jsonschema.Reflector{}
	return WireManifest{pluginsdk.Manifest{
		Name: "ssh", Version: "1.2.3", Title: "SSH", Description: "run",
		InputSchema: r.Reflect(&in{}), OutputSchema: r.Reflect(&in{}),
		Idempotent: true, Requires: []string{"bin:ssh"}, TargetMode: pluginsdk.TargetInventory,
	}}
}

func TestMessageRoundTrip(t *testing.T) {
	secrets := SecretBundle{"secret://a/b": {"password": "hunter2"}}
	tests := []struct {
		name string
		v    any
		new  func() any
	}{
		{"hello request", &HelloRequest{
			Protocol: 1, SessionSeq: 3, AgentVersion: "0.1.0", Pool: "default",
			Labels: map[string]string{"zone": "a"}, MaxConcurrent: 8,
			Capabilities: []string{"bin:git"}, Manifests: []WireManifest{sampleManifest()},
			Executions: []ExecutionState{{AttemptID: "a1", State: ExecRunning}, {AttemptID: "a2", State: ExecFinished}},
		}, func() any { return new(HelloRequest) }},
		{"hello response", &HelloResponse{
			Protocol: 1, HeartbeatIntervalMS: 10000,
			Limits:         Limits{MaxFrameBytes: MaxFrameSize, LogBatchBytes: 65536, LogBatchIntervalMS: 200},
			Reconciliation: []Reconciliation{{"a1", ActionContinue}, {"a2", ActionCancel}},
		}, func() any { return new(HelloResponse) }},
		{"heartbeat", &Heartbeat{RunningAttempts: []string{"a1"}, FreeSlots: 3, CPUPercent: 12.5, MemoryBytes: 1 << 33, Load1: 0.5, SpoolBytes: 42},
			func() any { return new(Heartbeat) }},
		{"step.execute", &StepExecute{
			AttemptID: "a1", ExecutionID: "e1", StepPath: "root/0", PluginName: "ssh", PluginVersion: "1.0.0",
			Input:     map[string]any{"cmd": "ls", "n": int64(3), "nested": map[string]any{"k": true}},
			Secrets:   secrets,
			Nodes:     map[string]pluginsdk.NodeInfo{"n1": {Name: "n1", Address: "10.0.0.1", Port: 22, Transport: "ssh", Credential: "secret://a/b", Attributes: map[string]any{"env": "prod"}}},
			TimeoutMS: 5000, Trace: TraceContext{Traceparent: "00-abc-def-01"},
		}, func() any { return new(StepExecute) }},
		{"step.execute result", &StepExecuteResult{Accepted: false, Reason: "draining"}, func() any { return new(StepExecuteResult) }},
		{"log.append", &LogAppend{AttemptID: "a1", SeqFrom: 7, Entries: []LogEntry{
			{TS: 1700000000000000000, Stream: pluginsdk.Stdout, Line: "hi"},
			{TS: 1700000000000000001, Stream: pluginsdk.System, Line: "gap"},
		}}, func() any { return new(LogAppend) }},
		{"log.ack", &LogAck{AttemptID: "a1", Seq: 9}, func() any { return new(LogAck) }},
		{"step.progress", &StepProgress{AttemptID: "a1", Percent: 50, Message: "half"}, func() any { return new(StepProgress) }},
		{"step.complete", &StepComplete{
			AttemptID: "a1", Status: StatusFailed, Outputs: map[string]any{"rc": int64(1)},
			Error:       &StepError{Code: "exit", Message: "boom", Retryable: true},
			StartedAtNS: 1, FinishedAtNS: 2,
		}, func() any { return new(StepComplete) }},
		{"step.complete ack", &StepCompleteAck{Acknowledged: true, Stale: true}, func() any { return new(StepCompleteAck) }},
		{"step.cancel", &StepCancel{AttemptID: "a1", GraceMS: 3000}, func() any { return new(StepCancel) }},
		{"step.cancel result", &StepCancelResult{Known: true}, func() any { return new(StepCancelResult) }},
		{"agent.drain", &Drain{Reason: "shutdown"}, func() any { return new(Drain) }},
		{"agent.reconnect", &Reconnect{JitterMS: 5000, Reason: "controller stopping"}, func() any { return new(Reconnect) }},
		{"node.hostkey_observed", &HostKeyObserved{AttemptID: "a1", Node: "n1", KeyType: "ssh-ed25519", HostKey: "ssh-ed25519 AAAA", Fingerprint: "SHA256:xyz"},
			func() any { return new(HostKeyObserved) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := Encode(tt.v)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			got := tt.new()
			if err := Decode(b, got); err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if h, ok := tt.v.(*HelloRequest); ok {
				assertManifests(t, h, got.(*HelloRequest))
				return
			}
			// Compare canonical encodings: decoded integers in any-typed
			// fields have different Go types than the originals.
			b2, err := Encode(got)
			if err != nil || !bytes.Equal(b, b2) {
				t.Fatalf("round trip mismatch:\n got %#v\nwant %#v (%v)", got, tt.v, err)
			}
		})
	}
}

func assertManifests(t *testing.T, want, got *HelloRequest) {
	t.Helper()
	if len(got.Manifests) != len(want.Manifests) {
		t.Fatalf("manifests: got %d want %d", len(got.Manifests), len(want.Manifests))
	}
	for i := range want.Manifests {
		w, err1 := want.Manifests[i].Checksum()
		g, err2 := got.Manifests[i].Checksum()
		if err1 != nil || err2 != nil || w != g {
			t.Fatalf("manifest %d checksum: %q vs %q (%v, %v)", i, g, w, err1, err2)
		}
	}
	want.Manifests, got.Manifests = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hello mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func TestLogEntryWireForm(t *testing.T) {
	b, err := Encode(LogEntry{TS: 5, Stream: pluginsdk.Stderr, Line: "x"})
	if err != nil {
		t.Fatal(err)
	}
	var arr []any
	if err := Decode(b, &arr); err != nil {
		t.Fatalf("log entry is not an array: %v", err)
	}
	if len(arr) != 3 || arr[1] != "stderr" || arr[2] != "x" {
		t.Fatalf("unexpected array form: %#v", arr)
	}
}

func TestMessageEnvelope(t *testing.T) {
	req, err := NewRequest(7, MethodStepCancel, StepCancel{AttemptID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	note, _ := NewNotification(MethodLogAck, LogAck{AttemptID: "a", Seq: 1})
	res, _ := NewResult(req.ID, StepCancelResult{Known: true})
	nilRes, _ := NewResult(req.ID, nil)

	tests := []struct {
		name               string
		m                  *Message
		isReq, isNote, isR bool
	}{
		{"request", req, true, false, false},
		{"notification", note, false, true, false},
		{"result", res, false, false, true},
		{"nil result", nilRes, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := EncodeMessage(tt.m)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodeMessage(b)
			if err != nil {
				t.Fatalf("DecodeMessage: %v", err)
			}
			if got.IsRequest() != tt.isReq || got.IsNotification() != tt.isNote || got.IsResponse() != tt.isR {
				t.Fatalf("kind mismatch: %+v", got)
			}
			if got.JSONRPC != "2.0" {
				t.Fatalf("jsonrpc = %q", got.JSONRPC)
			}
		})
	}
	// Params decode lazily.
	b, _ := EncodeMessage(req)
	got, _ := DecodeMessage(b)
	var sc StepCancel
	if err := (Params{got.Params}).Decode(&sc); err != nil || sc.AttemptID != "a" {
		t.Fatalf("params decode: %v %+v", err, sc)
	}
}

func TestDecodeMessageMalformed(t *testing.T) {
	good, _ := EncodeMessage(&Message{Method: "x"})
	wrongVer, _ := Encode(map[string]any{"jsonrpc": "1.0", "method": "x"})
	noVer, _ := Encode(map[string]any{"method": "x"})
	noID, _ := Encode(map[string]any{"jsonrpc": "2.0", "result": 1})
	notMap, _ := Encode([]int{1, 2})

	tests := []struct {
		name string
		in   []byte
		want error
	}{
		{"empty", nil, ErrBadFrame},
		{"garbage", []byte{0xc1, 0xff, 0x00}, ErrBadFrame},
		{"truncated", good[:len(good)-2], ErrBadFrame},
		{"not a map", notMap, ErrBadFrame},
		{"wrong version", wrongVer, ErrBadFrame},
		{"missing version", noVer, ErrBadFrame},
		{"response without id", noID, ErrBadFrame},
		{"oversized", bytes.Repeat([]byte{0}, MaxFrameSize+1), ErrFrameTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeMessage(tt.in); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestEncodeMessageOversized(t *testing.T) {
	m, err := NewNotification("x", map[string]string{"big": string(bytes.Repeat([]byte("a"), MaxFrameSize))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EncodeMessage(m); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}

func TestParamsDecodeErrors(t *testing.T) {
	var v LogAck
	err := (Params{}).Decode(&v)
	var re *RPCError
	if !errors.As(err, &re) || re.Code != CodeInvalidParams {
		t.Fatalf("missing params: %v", err)
	}
	err = (Params{[]byte{0xc1}}).Decode(&v)
	if !errors.As(err, &re) || re.Code != CodeInvalidParams {
		t.Fatalf("bad params: %v", err)
	}
}

func TestVersionNegotiation(t *testing.T) {
	tests := []struct {
		n, req  int
		want    int
		wantErr bool
	}{
		{1, 1, 1, false},
		{1, 0, 0, true},
		{1, 2, 0, true},
		{1, -1, 0, true},
		{2, 2, 2, false},
		{2, 1, 1, false},
		{2, 0, 0, true},
		{2, 3, 0, true},
		{5, 4, 4, false},
		{5, 3, 0, true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("N=%d req=%d", tt.n, tt.req), func(t *testing.T) {
			got, err := negotiate(tt.n, tt.req)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("got %d, %v", got, err)
			}
			if tt.wantErr && !errors.Is(err, ErrUnsupportedVersion) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	if v, err := Negotiate(ProtocolVersion); err != nil || v != 1 {
		t.Fatalf("Negotiate(current) = %d, %v", v, err)
	}
	if !reflect.DeepEqual(Supported(), []int{1}) {
		t.Fatalf("Supported() = %v", Supported())
	}
}
