package api

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"
)

// TestServeFailureSurfaces closes the listener under a running server, which
// makes Serve fail; the error must reach Err and not only the log.
func TestServeFailureSurfaces(t *testing.T) {
	var logs bytes.Buffer
	srv := NewServer(Config{
		Addr: "127.0.0.1:0", Store: nopChecker{},
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = srv.ln.Close()
	select {
	case err := <-srv.Err():
		if err == nil {
			t.Fatal("nil error from Err")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve failure not reported")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Stop(ctx); err != nil {
		t.Errorf("Stop after failure: %v", err)
	}
}

type nopChecker struct{}

func (nopChecker) Ping(context.Context) error                      { return nil }
func (nopChecker) MigrationVersion(context.Context) (int64, error) { return 1, nil }
