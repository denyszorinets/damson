package main

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

// TestControllerMountsAgentGateway checks the wiring: the connect endpoint is
// reachable, refuses plain ws:// on a controller and, in dev, gets past the
// transport check to token authentication.
func TestControllerMountsAgentGateway(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"controller refuses plain ws", []string{"controller", "-auto-migrate"}, http.StatusForbidden},
		{"controller with --insecure-dev needs a token", []string{"controller", "-auto-migrate", "-insecure-dev"}, http.StatusUnauthorized},
		{"dev needs a token", []string{"dev"}, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var errb syncBuf
			done := make(chan int, 1)
			env := ctrlEnv(t)
			go func() { done <- run(ctx, tt.args, io.Discard, &errb, env) }()
			base := "http://" + listenAddr(t, &errb)

			code, body := get(t, base+"/agent/v1/connect")
			if code != tt.want {
				t.Errorf("GET connect = %d %s, want %d", code, body, tt.want)
			}
			cancel()
			select {
			case c := <-done:
				if c != exitOK {
					t.Errorf("exit = %d; log: %s", c, errb.String())
				}
			case <-time.After(10 * time.Second):
				t.Fatal("did not shut down")
			}
		})
	}
}
