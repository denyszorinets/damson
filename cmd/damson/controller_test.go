package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	code := m.Run()
	stopPostgres()
	if code == 0 {
		if err := goleak.Find(); err != nil {
			fmt.Fprintf(os.Stderr, "goleak: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

// ctrlEnv points a controller or dev run at a temp SQLite file and a free port.
func ctrlEnv(t *testing.T) []string {
	t.Helper()
	return []string{
		"DAMSON_DATABASE__DSN=" + filepath.Join(t.TempDir(), "d.db"),
		"DAMSON_CONTROLLER__LISTEN=127.0.0.1:0",
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// listenAddr waits for the "http server listening" log line and returns addr.
func listenAddr(t *testing.T, buf *syncBuf) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for line := range strings.SplitSeq(buf.String(), "\n") {
			var rec struct {
				Msg  string `json:"msg"`
				Addr string `json:"addr"`
			}
			if json.Unmarshal([]byte(line), &rec) == nil && rec.Msg == "http server listening" {
				return rec.Addr
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never listened; log: %s", buf.String())
	return ""
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{}}
	defer client.CloseIdleConnections()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestControllerServes(t *testing.T) {
	tests := []struct {
		name      string
		role      string
		args      []string
		wantReady int
	}{
		{"controller without auto-migrate is not ready", "controller", nil, 503},
		{"controller with auto-migrate is ready", "controller", []string{"-auto-migrate"}, 200},
		{"dev migrates and is ready", "dev", nil, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var errb syncBuf
			env := ctrlEnv(t)
			done := make(chan int, 1)
			go func() {
				done <- run(ctx, append([]string{tt.role}, tt.args...), io.Discard, &errb, env)
			}()
			base := "http://" + listenAddr(t, &errb)

			if code, _ := get(t, base+"/healthz"); code != 200 {
				t.Errorf("/healthz = %d", code)
			}
			if code, _ := get(t, base+"/readyz"); code != tt.wantReady {
				t.Errorf("/readyz = %d, want %d", code, tt.wantReady)
			}
			if code, body := get(t, base+"/api/v1/x"); code != 404 || !strings.Contains(body, "problem") {
				t.Errorf("/api/v1/x = %d %s", code, body)
			}

			cancel()
			select {
			case code := <-done:
				if code != exitOK {
					t.Errorf("exit = %d; log: %s", code, errb.String())
				}
			case <-time.After(10 * time.Second):
				t.Fatal("did not shut down")
			}
			if strings.Contains(errb.String(), strings.TrimPrefix(env[0], "DAMSON_DATABASE__DSN=")) {
				t.Errorf("DSN leaked: %s", errb.String())
			}
		})
	}
}

func TestControllerBadStore(t *testing.T) {
	var errb bytes.Buffer
	env := []string{"DAMSON_DATABASE__DSN=/nonexistent-dir/x/d.db", "DAMSON_CONTROLLER__LISTEN=127.0.0.1:0"}
	if code := run(context.Background(), []string{"controller"}, io.Discard, &errb, env); code != exitError {
		t.Errorf("exit = %d, want %d; stderr %s", code, exitError, errb.String())
	}
}

func TestControllerBadStoreDoesNotLogPassword(t *testing.T) {
	var errb syncBuf
	env := []string{
		"DAMSON_DATABASE__DRIVER=postgres",
		"DAMSON_DATABASE__DSN=postgres://u:s3cret@127.0.0.1:1/db?connect_timeout=1",
		"DAMSON_CONTROLLER__LISTEN=127.0.0.1:0",
	}
	var out bytes.Buffer
	if code := run(context.Background(), []string{"controller"}, &out, &errb, env); code != exitError {
		t.Errorf("exit = %d, want %d; stderr %s", code, exitError, errb.String())
	}
	if strings.Contains(errb.String(), "s3cret") || strings.Contains(out.String(), "s3cret") {
		t.Errorf("password leaked: %s", errb.String())
	}
}
