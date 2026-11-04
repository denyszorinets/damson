// Package internalapi is the HTTP endpoint other controllers call: the live log
// tail of an execution this controller owns (ARCHITECTURE 8.5, ADR-0040). It
// listens on its own port and every request is authenticated with the cluster
// secret. It serves only this controller's own buffers and never forwards.
package internalapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/controller/internalauth"
)

// Wire format of the tail: one JSON object per line.
const (
	ContentType = "application/x-ndjson"
	TypeLine    = "line"
	TypeBeat    = "heartbeat"
	TailPath    = "/internal/v1/executions/%s/tail"
)

// Item is one line of the tail response.
type Item struct {
	Type   string    `json:"type"`
	Seq    int64     `json:"seq,omitempty"`
	TS     time.Time `json:"ts,omitzero"`
	Stream string    `json:"stream,omitempty"`
	Line   string    `json:"line,omitempty"`
}

// Defaults.
const (
	DefaultHeartbeat = 10 * time.Second
	DefaultMaxTails  = 256
)

// Tail is this controller's live log buffer.
type Tail interface {
	Subscribe(ctx context.Context, executionID string, afterSeq int64) (<-chan api.LogLine, error)
	// Attempt is the attempt buffered for the execution, "" when none.
	Attempt(executionID string) string
}

// Config configures a Server.
type Config struct {
	Keys *internalauth.Keyring
	Self string // this controller's id
	Tail Tail
	// Heartbeat is the idle keep-alive interval; MaxTails bounds concurrent
	// tails. Zero values select the defaults.
	Heartbeat time.Duration
	MaxTails  int
	Now       func() time.Time
	Logger    *slog.Logger
}

// Server is the internal listener.
type Server struct {
	cfg   Config
	log   *slog.Logger
	http  *http.Server
	slots chan struct{}

	handlers sync.WaitGroup // tail handlers in flight
	lastWarn atomic.Int64   // unix nanoseconds of the last rejection warning

	mu   sync.Mutex
	done chan struct{}
}

// New validates cfg.
func New(cfg Config) (*Server, error) {
	if cfg.Keys == nil || cfg.Self == "" || cfg.Tail == nil {
		return nil, errors.New("internalapi: keys, self and tail are required")
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = DefaultHeartbeat
	}
	if cfg.MaxTails <= 0 {
		cfg.MaxTails = DefaultMaxTails
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &Server{cfg: cfg, log: log, slots: make(chan struct{}, cfg.MaxTails)}
	s.http = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute}
	return s, nil
}

// Handler serves the internal routes; everything else is 404.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/v1/executions/{id}/tail", s.authed(s.tail))
	return mux
}

// authed rejects a request that does not verify. The answer is a bare 401: it
// says nothing about why, and the log names only the caller and a reason.
func (s *Server) authed(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, err := s.cfg.Keys.Verify(r, s.cfg.Self, s.cfg.Now())
		if err != nil {
			s.warnRejected(err, r)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r, caller)
	}
}

// warnRejected logs a rejected request, at most once every 10 seconds: the
// caller header is unauthenticated, so it is truncated and the rate is bounded
// against a client that floods the port.
func (s *Server) warnRejected(reason error, r *http.Request) {
	now := time.Now().UnixNano()
	if last := s.lastWarn.Load(); now-last < int64(10*time.Second) || !s.lastWarn.CompareAndSwap(last, now) {
		return
	}
	caller := r.Header.Get(internalauth.HeaderCaller)
	if len(caller) > 64 {
		caller = caller[:64]
	}
	s.log.Warn("internal request rejected", "reason", reason.Error(), "caller_id", caller)
}

// tail streams the buffered lines after after_seq and then the live ones. 404
// means this controller has no buffer for the execution; 409 means it buffers
// another attempt than the caller expects (sequence numbers restart per
// attempt). Either way the caller reads the stored chunks instead.
func (s *Server) tail(w http.ResponseWriter, r *http.Request, caller string) {
	s.handlers.Add(1)
	defer s.handlers.Done()
	id, attempt := r.PathValue("id"), r.URL.Query().Get("attempt_id")
	after, err := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	if r.URL.Query().Get("after_seq") == "" {
		after, err = 0, nil
	}
	if err != nil || after < 0 || attempt == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		http.Error(w, "too many tails", http.StatusServiceUnavailable)
		return
	}
	switch have := s.cfg.Tail.Attempt(id); {
	case have == "":
		http.Error(w, "no live tail", http.StatusNotFound)
		return
	case have != attempt:
		http.Error(w, "attempt mismatch", http.StatusConflict)
		return
	}
	ch, err := s.cfg.Tail.Subscribe(r.Context(), id, after)
	if err != nil {
		http.Error(w, "no live tail", http.StatusNotFound)
		return
	}
	s.log.Debug("internal tail opened", "execution_id", id, "caller_id", caller, "after_seq", after)
	w.Header().Set("Content-Type", ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.Flush() // headers out now: the caller waits for them with a short timeout
	enc := json.NewEncoder(w)
	beat := time.NewTicker(s.cfg.Heartbeat)
	defer beat.Stop()
	for {
		var item Item
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			item = Item{Type: TypeBeat}
		case ln, ok := <-ch:
			if !ok {
				return // finished, or this subscriber fell behind: the caller polls
			}
			item = Item{Type: TypeLine, Seq: ln.Seq, TS: ln.TS, Stream: ln.Stream, Line: ln.Line}
		}
		_ = rc.SetWriteDeadline(time.Now().Add(2 * s.cfg.Heartbeat))
		if enc.Encode(item) != nil || rc.Flush() != nil {
			return
		}
	}
}

// Serve accepts on ln in the background until Stop.
func (s *Server) Serve(ln net.Listener) {
	s.mu.Lock()
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()
	go func() {
		defer close(done)
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("internal listener failed", "error", err)
		}
	}()
}

// Stop closes the listener and the open tails and waits for the server.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	// Tails are long-lived streams: Shutdown would wait for them, so close.
	if err := s.http.Close(); err != nil {
		return fmt.Errorf("closing internal listener: %w", err)
	}
	handlersDone := make(chan struct{})
	go func() { s.handlers.Wait(); close(handlersDone) }()
	for _, c := range []chan struct{}{done, handlersDone} {
		select {
		case <-c:
		case <-ctx.Done():
			return fmt.Errorf("waiting for internal listener: %w", ctx.Err())
		}
	}
	return nil
}
