package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/agent"
)

// TestAwaitShutdownReturnsFatalDevAgentError checks that dev shuts down with
// the agent's error when the in-process agent is refused for good.
func TestAwaitShutdownReturnsFatalDevAgentError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	a, err := agent.New(agent.Config{
		ControllerURL: "ws" + srv.URL[len("http"):], Token: "t", AgentID: "a", Pool: "p",
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Stop(context.Background()) }()

	done := make(chan error, 1)
	go func() {
		done <- awaitShutdown(context.Background(), slog.New(slog.DiscardHandler), make(chan error), a)
	}()
	select {
	case err := <-done:
		var se *agent.StatusError
		if !errors.As(err, &se) || se.Status != http.StatusUnauthorized {
			t.Errorf("err = %v, want wrapped HTTP 401", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("dev kept running after its agent ended")
	}
}

func TestAwaitShutdownOnContextAndServerError(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := awaitShutdown(ctx, log, make(chan error), nil); err != nil {
		t.Errorf("cancelled ctx = %v, want nil", err)
	}
	errc := make(chan error, 1)
	boom := errors.New("boom")
	errc <- boom
	if err := awaitShutdown(context.Background(), log, errc, nil); !errors.Is(err, boom) {
		t.Errorf("server error = %v", err)
	}
}
