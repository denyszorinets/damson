package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

func TestFailureMapping(t *testing.T) {
	tests := []struct {
		name      string
		plugin    testPlugin
		wantState string
		wantCode  string
		retryable bool
		wantMsg   string
	}{
		{
			name: "result error keeps the kind",
			plugin: plug(func(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error) {
				return pluginsdk.Result{Error: pluginsdk.ExitCodeError(3, "script exited 3")}, nil
			}),
			wantState: protocol.StatusFailed, wantCode: "exit_code", wantMsg: "script exited 3",
		},
		{
			name: "connection errors are retryable",
			plugin: plug(func(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error) {
				return pluginsdk.Result{}, pluginsdk.NewError(pluginsdk.KindConnection, "dial failed", nil)
			}),
			wantState: protocol.StatusFailed, wantCode: "connection", retryable: true, wantMsg: "dial failed",
		},
		{
			name: "plain error is internal",
			plugin: plug(func(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error) {
				return pluginsdk.Result{}, errors.New("boom")
			}),
			wantState: protocol.StatusFailed, wantCode: "internal", wantMsg: "boom",
		},
		{
			name: "panic is recovered as internal",
			plugin: plug(func(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error) {
				panic("kaboom")
			}),
			wantState: protocol.StatusFailed, wantCode: "internal", wantMsg: "plugin panicked",
		},
		{
			name: "validate failure never executes",
			plugin: testPlugin{
				name:     "demo",
				validate: func(context.Context, json.RawMessage) error { return pluginsdk.ValidationError("bad input") },
				exec: func(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error) {
					panic("Execute must not run after a failed Validate")
				},
			},
			wantState: protocol.StatusFailed, wantCode: "validation", wantMsg: "bad input",
		},
		{
			name: "outputs that are not an object",
			plugin: plug(func(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error) {
				return pluginsdk.Result{Outputs: json.RawMessage(`[1,2]`)}, nil
			}),
			wantState: protocol.StatusFailed, wantCode: "internal", wantMsg: "plugin outputs are not a JSON object",
		},
		{
			name: "success with empty outputs",
			plugin: plug(func(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error) {
				return pluginsdk.Result{}, nil
			}),
			wantState: protocol.StatusSucceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctl := newFakeCtl(t)
			startAgent(t, ctl, []pluginsdk.Plugin{tt.plugin})
			ctl.mustExecute(stepReq("a1"))
			c := completeOf(t, ctl, "a1")
			if c.Status != tt.wantState {
				t.Fatalf("status = %q, want %q (error %+v)", c.Status, tt.wantState, c.Error)
			}
			if tt.wantState == protocol.StatusSucceeded {
				if c.Error != nil {
					t.Errorf("error = %+v", c.Error)
				}
				return
			}
			if c.Error == nil || c.Error.Code != tt.wantCode || c.Error.Message != tt.wantMsg || c.Error.Retryable != tt.retryable {
				t.Errorf("error = %+v, want code %q message %q retryable %v", c.Error, tt.wantCode, tt.wantMsg, tt.retryable)
			}
		})
	}
}

func TestPluginErrorAfterCancelIsCancelled(t *testing.T) {
	// A plugin that reports its own failure once ctx is cancelled (for
	// example an exit-code error from a killed process) is still a cancel.
	ctl := newFakeCtl(t)
	started := make(chan struct{})
	p := plug(func(ctx context.Context, _ pluginsdk.ExecRequest, _ pluginsdk.Emitter) (pluginsdk.Result, error) {
		close(started)
		<-ctx.Done()
		return pluginsdk.Result{Error: pluginsdk.ExitCodeError(137, "killed")}, nil
	})
	startAgent(t, ctl, []pluginsdk.Plugin{p})
	ctl.mustExecute(stepReq("a1"))
	<-started
	ctl.cancel("a1", 1000)
	if c := completeOf(t, ctl, "a1"); c.Status != protocol.StatusCanceled {
		t.Errorf("status = %q, want canceled", c.Status)
	}
}
