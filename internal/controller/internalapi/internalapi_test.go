package internalapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/controller/internalauth"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

const secret = "0123456789abcdef0123456789abcdef-cluster"

// fakeTail serves a fixed attempt and lines from a channel the test feeds.
type fakeTail struct {
	attempt string
	replay  []api.LogLine
	live    chan api.LogLine
}

func (f *fakeTail) Attempt(string) string { return f.attempt }

func (f *fakeTail) Subscribe(ctx context.Context, _ string, after int64) (<-chan api.LogLine, error) {
	if f.attempt == "" {
		return nil, api.ErrNoLiveTail
	}
	out := make(chan api.LogLine, 16)
	for _, l := range f.replay {
		if l.Seq > after {
			out <- l
		}
	}
	go func() {
		defer close(out)
		for {
			select {
			case l, ok := <-f.live:
				if !ok {
					return
				}
				out <- l
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func setup(t *testing.T, ft *fakeTail) (*httptest.Server, *internalauth.Keyring, *syncBuf) {
	t.Helper()
	keys, err := internalauth.NewKeyring(secret, "")
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuf{}
	s, err := New(Config{Keys: keys, Self: "ctl-B", Tail: ft, Heartbeat: 50 * time.Millisecond, Logger: slog.New(slog.NewJSONHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, keys, logs
}

func get(t *testing.T, srv *httptest.Server, keys *internalauth.Keyring, target, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if keys != nil {
		keys.Sign(req, "ctl-A", target, time.Now())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestTailStreamsReplayThenLive(t *testing.T) {
	ft := &fakeTail{attempt: "a1", replay: []api.LogLine{{Seq: 1, Line: "one"}, {Seq: 2, Line: "two"}}, live: make(chan api.LogLine)}
	srv, keys, _ := setup(t, ft)
	resp := get(t, srv, keys, "ctl-B", "/internal/v1/executions/e1/tail?attempt_id=a1&after_seq=1")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != ContentType {
		t.Fatalf("status %d type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	sc := bufio.NewScanner(resp.Body)
	next := func() Item {
		t.Helper()
		if !sc.Scan() {
			t.Fatalf("stream ended: %v", sc.Err())
		}
		var it Item
		if err := json.Unmarshal(sc.Bytes(), &it); err != nil {
			t.Fatal(err)
		}
		return it
	}
	if it := next(); it.Type != TypeLine || it.Seq != 2 || it.Line != "two" {
		t.Fatalf("replay item = %+v; want seq 2 only", it)
	}
	ft.live <- api.LogLine{Seq: 3, Stream: "out", Line: "three"}
	for { // heartbeats may come first
		it := next()
		if it.Type == TypeBeat {
			continue
		}
		if it.Seq != 3 || it.Line != "three" || it.Stream != "out" {
			t.Fatalf("live item = %+v", it)
		}
		break
	}
	close(ft.live) // the execution finished: the stream ends
	if sc.Scan() {
		var it Item
		_ = json.Unmarshal(sc.Bytes(), &it)
		if it.Type != TypeBeat {
			t.Fatalf("item after the end: %s", sc.Text())
		}
	}
}

func TestTailRejectsAndReports(t *testing.T) {
	good := "/internal/v1/executions/e1/tail?attempt_id=a1"
	tests := []struct {
		name   string
		ft     *fakeTail
		keys   func(*internalauth.Keyring) *internalauth.Keyring
		target string
		path   string
		want   int
	}{
		{"unsigned", &fakeTail{attempt: "a1"}, func(*internalauth.Keyring) *internalauth.Keyring { return nil }, "", good, 401},
		{"signed for another controller", &fakeTail{attempt: "a1"}, nil, "ctl-C", good, 401},
		{"wrong secret", &fakeTail{attempt: "a1"}, func(*internalauth.Keyring) *internalauth.Keyring {
			k, _ := internalauth.NewKeyring(strings.Repeat("x", 40), "")
			return k
		}, "ctl-B", good, 401},
		{"no live buffer", &fakeTail{}, nil, "ctl-B", good, 404},
		{"another attempt", &fakeTail{attempt: "a2"}, nil, "ctl-B", good, 409},
		{"no attempt", &fakeTail{attempt: "a1"}, nil, "ctl-B", "/internal/v1/executions/e1/tail", 400},
		{"bad after_seq", &fakeTail{attempt: "a1"}, nil, "ctl-B", good + "&after_seq=-1", 400},
		{"unknown route", &fakeTail{attempt: "a1"}, nil, "ctl-B", "/internal/v1/other", 404},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, keys, _ := setup(t, tc.ft)
			if tc.keys != nil {
				keys = tc.keys(keys)
			}
			if resp := get(t, srv, keys, tc.target, tc.path); resp.StatusCode != tc.want {
				t.Fatalf("status = %d; want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// The secret and the signature never reach the log or a response body.
func TestSecretsAreNeitherLoggedNorReturned(t *testing.T) {
	srv, keys, logs := setup(t, &fakeTail{attempt: "a1"})
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/internal/v1/executions/e1/tail?attempt_id=a1", nil)
	keys.Sign(req, "ctl-A", "ctl-B", time.Now().Add(-time.Hour)) // expired: rejected and logged
	sig := req.Header.Get(internalauth.HeaderSignature)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body bytes.Buffer
	_, _ = body.ReadFrom(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	for _, leak := range []string{secret, sig} {
		if strings.Contains(logs.String(), leak) || strings.Contains(body.String(), leak) {
			t.Fatalf("%q leaked:\nlog: %s\nbody: %s", leak, logs.String(), body.String())
		}
	}
	if !strings.Contains(logs.String(), "ctl-A") || !strings.Contains(logs.String(), "timestamp") {
		t.Fatalf("rejection not logged with caller and reason: %s", logs.String())
	}
}

func TestTooManyTails(t *testing.T) {
	keys, _ := internalauth.NewKeyring(secret, "")
	ft := &fakeTail{attempt: "a1", live: make(chan api.LogLine)}
	s, err := New(Config{Keys: keys, Self: "ctl-B", Tail: ft, MaxTails: 1, Heartbeat: time.Hour, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	first := get(t, srv, keys, "ctl-B", "/internal/v1/executions/e1/tail?attempt_id=a1")
	if first.StatusCode != 200 {
		t.Fatal(first.StatusCode)
	}
	if second := get(t, srv, keys, "ctl-B", "/internal/v1/executions/e2/tail?attempt_id=a1"); second.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second tail = %d; want 503", second.StatusCode)
	}
	close(ft.live)
}
