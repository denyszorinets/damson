package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		env      []string
		cancel   bool
		wantCode int
		wantOut  string
		wantErr  string
		readyLog string // log text that signals startup finished
	}{
		{name: "no args", wantCode: exitUsage, wantErr: "Usage:"},
		{name: "unknown", args: []string{"nope"}, wantCode: exitUsage, wantErr: "unknown command"},
		{name: "version", args: []string{"version"}, wantCode: exitOK, wantOut: "damson"},
		{name: "controller starts and stops", args: []string{"controller"}, env: ctrlEnv(t), cancel: true, wantCode: exitOK, wantErr: `"starting"`, readyLog: "http server listening"},
		{name: "dev starts and stops", args: []string{"dev"}, env: ctrlEnv(t), cancel: true, wantCode: exitOK, wantErr: `"role":"dev"`, readyLog: "http server listening"},
		{name: "agent without config fails", args: []string{"agent"}, wantCode: exitError, wantErr: "agent.token"},
		{
			name: "agent redacts token", cancel: true, wantCode: exitOK, wantErr: `"starting"`, readyLog: `"starting"`,
			args: []string{"agent"},
			env:  []string{"DAMSON_AGENT__CONTROLLER_URL=ws://h", "DAMSON_AGENT__TOKEN=topsecret"},
		},
		{name: "bad flag", args: []string{"agent", "-bogus"}, wantCode: exitUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var out bytes.Buffer
			var errb syncBuf
			if tt.cancel {
				go func() {
					waitLog(&errb, tt.readyLog)
					cancel()
				}()
			}
			if got := run(ctx, tt.args, &out, &errb, tt.env); got != tt.wantCode {
				t.Fatalf("exit = %d, want %d; stderr: %s", got, tt.wantCode, errb.String())
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("stdout %q missing %q", out.String(), tt.wantOut)
			}
			if !strings.Contains(errb.String(), tt.wantErr) {
				t.Errorf("stderr %q missing %q", errb.String(), tt.wantErr)
			}
			if strings.Contains(errb.String(), "topsecret") {
				t.Errorf("token leaked into logs: %s", errb.String())
			}
		})
	}
}

// waitLog blocks until buf contains substr (or 10s pass), so tests cancel
// only once the role has actually started.
func waitLog(buf *syncBuf, substr string) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(buf.String(), substr) {
		time.Sleep(5 * time.Millisecond)
	}
}
