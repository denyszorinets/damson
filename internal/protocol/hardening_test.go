package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// logged runs one peer on conn with a captured log.
func logged(t *testing.T, conn Conn, configure func(*Peer)) (*Peer, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	p := NewPeer(conn, slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if configure != nil {
		configure(p)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()
	t.Cleanup(func() { cancel(); _ = p.Close(); <-done })
	return p, buf
}

func TestMessageAndParamsAreMasked(t *testing.T) {
	const secret = "hunter2-very-secret"
	req, err := NewRequest(7, MethodStepExecute, StepExecute{
		AttemptID: "a1", Secrets: SecretBundle{"secret://a/b": {"password": secret}},
	})
	if err != nil {
		t.Fatal(err)
	}
	params := Params{req.Params}
	if !bytes.Contains(req.Params, []byte(secret)) {
		t.Fatal("test premise: wire params must carry the value")
	}

	var logJSON, logText bytes.Buffer
	slog.New(slog.NewJSONHandler(&logJSON, nil)).Info("m", "msg", req, "val", *req, "params", params)
	slog.New(slog.NewTextHandler(&logText, nil)).Info("m", "msg", req, "val", *req, "params", params)
	outputs := map[string]string{"slog json": logJSON.String(), "slog text": logText.String()}
	for _, v := range []any{req, *req, params} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			outputs[fmt.Sprintf("%T %s", v, verb)] = fmt.Sprintf(verb, v)
		}
	}
	outputs["String"] = req.String() + params.String()
	for name, out := range outputs {
		if strings.Contains(out, secret) || strings.Contains(out, "password") {
			t.Errorf("%s leaks payload: %s", name, out)
		}
	}
	for _, want := range []string{MethodStepExecute, "request"} {
		if !strings.Contains(outputs["%v"+""], want) && !strings.Contains(fmt.Sprintf("%v", req), want) {
			t.Errorf("rendering lacks %q", want)
		}
	}
}

func TestRequestHandlerPanicIsContained(t *testing.T) {
	ca, cb := newPipe()
	a := startOne(t, ca)
	b, logs := logged(t, cb, nil)
	b.Handle("boom", func(context.Context, Params) (any, error) { panic("kaboom") })
	echoHandlers(b)

	err := a.Call(context.Background(), "boom", nil, nil)
	var re *RPCError
	if !errors.As(err, &re) || re.Code != CodeInternalError || re.Message != "internal error" {
		t.Fatalf("err = %v", err)
	}
	if err := a.Call(context.Background(), "echo", map[string]int{"x": 1}, nil); err != nil {
		t.Fatalf("connection should survive: %v", err)
	}
	if strings.Contains(logs.String(), "kaboom") || !strings.Contains(logs.String(), "panic_type=string") {
		t.Fatalf("panic must be logged by type only: %s", logs.String())
	}
}

func TestNotificationHandlerPanicIsContained(t *testing.T) {
	ca, cb := newPipe()
	a := startOne(t, ca)
	b, logs := logged(t, cb, nil)
	got := make(chan struct{})
	b.Handle("boom", func(context.Context, Params) (any, error) { panic("kaboom") })
	b.Handle("ok", func(context.Context, Params) (any, error) { close(got); return nil, nil })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Notify(ctx, "boom", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Notify(ctx, "ok", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("read loop died after a notification panic")
	}
	if strings.Contains(logs.String(), "kaboom") || !strings.Contains(logs.String(), "panic_type=string") {
		t.Fatalf("panic must be logged by type only: %s", logs.String())
	}
}

func TestPlainHandlerErrorIsGenericOnWire(t *testing.T) {
	ca, cb := newPipe()
	a := startOne(t, ca)
	b, logs := logged(t, cb, nil)
	b.Handle("fail", func(context.Context, Params) (any, error) {
		return nil, errors.New("dial postgres://user:pw@db: refused")
	})
	b.Handle("badparams", func(_ context.Context, p Params) (any, error) {
		var v struct{ X int }
		return nil, p.Decode(&v)
	})

	err := a.Call(context.Background(), "fail", nil, nil)
	var re *RPCError
	if !errors.As(err, &re) || re.Code != CodeInternalError || re.Message != "internal error" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(logs.String(), "postgres://user:pw@db") || !strings.Contains(logs.String(), "method=fail") ||
		!strings.Contains(logs.String(), "request_id=1") {
		t.Fatalf("detail not logged locally: %s", logs.String())
	}

	err = a.Call(context.Background(), "badparams", "not a map", nil)
	if !errors.As(err, &re) || re.Code != CodeInvalidParams || re.Message != "invalid params" {
		t.Fatalf("err = %v", err)
	}
}

func rawRequest(t *testing.T, id uint64, method string) []byte {
	t.Helper()
	req, err := NewRequest(id, method, nil)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := EncodeMessage(req)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func TestRequestOverloadIsRejectedWithoutHandler(t *testing.T) {
	ca, cb := newPipe()
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	var calls int
	var mu sync.Mutex
	logged(t, cb, func(p *Peer) {
		p.SetMaxInFlight(1)
		p.Handle("slow", func(context.Context, Params) (any, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			entered <- struct{}{}
			<-release
			return "done", nil
		})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ca.WriteFrame(ctx, rawRequest(t, 1, "slow")); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := ca.WriteFrame(ctx, rawRequest(t, 2, "slow")); err != nil {
		t.Fatal(err)
	}
	frame, err := ca.ReadFrame(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m, err := DecodeMessage(frame)
	if err != nil || m.Error == nil || m.Error.Code != CodeServerOverloaded {
		t.Fatalf("reply = %v, %v", m, err)
	}
	if id := idString(m.ID); id != "2" {
		t.Fatalf("overload reply id = %s", id)
	}
	close(release)
	frame, _ = ca.ReadFrame(ctx)
	if m, err := DecodeMessage(frame); err != nil || m.Error != nil || idString(m.ID) != "1" {
		t.Fatalf("first reply = %v, %v", m, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("handler ran %d times, want 1", calls)
	}
}

func TestNullIDRequestIsInvalid(t *testing.T) {
	ca, cb := newPipe()
	b := startOne(t, cb)
	echoHandlers(b)
	frame, err := EncodeMessage(&Message{ID: msgpack.RawMessage{0xc0}, Method: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ca.WriteFrame(ctx, frame); err != nil {
		t.Fatal(err)
	}
	reply, err := ca.ReadFrame(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m Message
	if err := Decode(reply, &m); err != nil || m.Error == nil || m.Error.Code != CodeInvalidRequest {
		t.Fatalf("reply = %v, %v", m, err)
	}
}

func TestResponsesWithoutUsableIDAreInvalid(t *testing.T) {
	for name, id := range map[string]msgpack.RawMessage{"null": {0xc0}, "absent": nil} {
		t.Run(name, func(t *testing.T) {
			frame, err := EncodeMessage(&Message{ID: id, Error: &RPCError{Code: CodeParseError, Message: "x"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeMessage(frame); !errors.Is(err, ErrNullIDResponse) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestNullIDErrorResponseIsNotAnswered(t *testing.T) {
	ca, cb := newPipe()
	b := startOne(t, cb)
	echoHandlers(b)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	frame, _ := EncodeMessage(&Message{ID: msgpack.RawMessage{0xc0}, Error: &RPCError{Code: CodeParseError, Message: "x"}})
	if err := ca.WriteFrame(ctx, frame); err != nil {
		t.Fatal(err)
	}
	// The next reply must belong to the next request, not to the error.
	req := rawRequest(t, 5, "echo")
	if err := ca.WriteFrame(ctx, req); err != nil {
		t.Fatal(err)
	}
	reply, _ := ca.ReadFrame(ctx)
	var m Message
	if err := Decode(reply, &m); err != nil || idString(m.ID) != "5" {
		t.Fatalf("reply = %v, %v", m, err)
	}
}

func TestRunTwice(t *testing.T) {
	ca, _ := newPipe()
	p := startOne(t, ca)
	// Wait until the first Run is certainly in progress.
	deadline := time.Now().Add(5 * time.Second)
	for !p.running.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := p.Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Run = %v", err)
	}
}

func TestCloseBeforeRun(t *testing.T) {
	ca, _ := newPipe()
	p := NewPeer(ca, quiet())
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- p.Run(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after Close = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run blocked after Close")
	}
	if err := p.Call(context.Background(), "x", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("Call = %v", err)
	}
}

func TestLogEntryLength(t *testing.T) {
	for _, js := range []string{`[1,"stdout"]`, `[]`} {
		var e LogEntry
		if err := json.Unmarshal([]byte(js), &e); err == nil {
			t.Errorf("json %s accepted", js)
		}
	}
	var je LogEntry
	if err := json.Unmarshal([]byte(`[1,"stdout","l",4,{"a":1}]`), &je); err != nil || je.TS != 1 || je.Line != "l" {
		t.Errorf("json extras: %+v, %v", je, err)
	}
	b, err := Encode([]any{1, "stdout"})
	if err != nil {
		t.Fatal(err)
	}
	var e LogEntry
	if err := Decode(b, &e); err == nil {
		t.Error("msgpack short accepted")
	}
	b, _ = Encode([]any{1, "stdout", "l", 4, map[string]int{"a": 1}})
	e = LogEntry{}
	if err := Decode(b, &e); err != nil || e.TS != 1 || e.Line != "l" {
		t.Errorf("msgpack extras: %+v, %v", e, err)
	}
	b, _ = Encode(LogEntry{TS: 1 << 60, Stream: "stdout", Line: "hi"})
	e = LogEntry{}
	if err := Decode(b, &e); err != nil || e.TS != 1<<60 || e.Line != "hi" {
		t.Fatalf("round trip = %+v, %v", e, err)
	}
}

func TestConcurrentCloseDoesNotBlock(t *testing.T) {
	a, _ := wsPair(t)
	var wg sync.WaitGroup
	done := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = a.Close() }()
	}
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent Close blocked")
	}
}
