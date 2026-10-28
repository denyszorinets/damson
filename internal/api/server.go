package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Config configures a Server.
type Config struct {
	Addr  string       // host:port; "127.0.0.1:0" picks a free port
	Store Checker      // readiness checks
	UI    http.Handler // served for every path the API does not claim
	// Mounts maps extra mux patterns (for example "/agent/v1/connect") to
	// handlers owned by other components. They are wrapped by the same
	// request logging as the API.
	Mounts map[string]http.Handler
	Logger *slog.Logger
}

// Server is the controller's HTTP server component. Start listens and serves
// in the background; Stop drains in-flight requests.
type Server struct {
	cfg  Config
	http *http.Server
	ln   net.Listener
	done chan struct{}
	errs chan error // buffered(1); receives a post-start Serve failure
}

// NewServer builds the server and its routes. Nothing listens until Start.
func NewServer(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Server{
		cfg: cfg,
		http: &http.Server{
			Handler:           logRequests(cfg.Logger, routes(cfg)),
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			// No WriteTimeout: SSE streams (later milestones) are long-lived.
			IdleTimeout: 2 * time.Minute,
			ErrorLog:    slog.NewLogLogger(cfg.Logger.Handler(), slog.LevelWarn),
		},
		done: make(chan struct{}),
		errs: make(chan error, 1),
	}
}

// routes wires the mux. The /api/ catch-all keeps unknown API paths out of the
// SPA fallback so clients always get problem+json.
func routes(cfg Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("/readyz", readyz(cfg.Store))
	mux.HandleFunc("/api/", notFound)
	for pattern, h := range cfg.Mounts {
		mux.Handle(pattern, h)
	}
	if cfg.UI != nil {
		mux.Handle("/", cfg.UI)
	}
	return mux
}

// Handler returns the full handler chain, for tests.
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Start binds the listen address and serves in a goroutine. It returns once
// the socket is bound, so bind errors surface here.
func (s *Server) Start(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.cfg.Addr, err)
	}
	s.ln = ln
	s.cfg.Logger.Info("http server listening", "addr", ln.Addr().String())
	go func() {
		defer close(s.done)
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.cfg.Logger.Error("http server failed", "error", err)
			s.errs <- fmt.Errorf("serving http: %w", err)
		}
	}()
	return nil
}

// Err receives at most one value: a failure of the serve loop after Start. A
// clean Stop sends nothing.
func (s *Server) Err() <-chan error { return s.errs }

// Addr returns the bound address, or "" before Start.
func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Stop stops accepting connections and waits for in-flight requests until ctx
// expires, then closes the rest. It is safe to call without Start.
func (s *Server) Stop(ctx context.Context) error {
	if s.ln == nil {
		return nil
	}
	err := s.http.Shutdown(ctx)
	if err != nil {
		_ = s.http.Close()
	}
	<-s.done
	if err != nil {
		return fmt.Errorf("shutting down http server: %w", err)
	}
	return nil
}
