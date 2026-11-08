package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/denyszorinets/damson/internal/expr"
)

// LogReader reads a log chunk object from the named backend (logblob.Set).
type LogReader interface {
	Get(ctx context.Context, backend, key string) ([]byte, error)
}

// Config configures a Server.
type Config struct {
	Addr  string    // host:port; "127.0.0.1:0" picks a free port
	Store Checker   // readiness checks
	Data  DataStore // typed /api/v1 endpoints; nil answers them 503
	// Plugins serves /api/v1/plugins; nil answers 503.
	Plugins PluginCatalog
	UI      http.Handler // served for every path the API does not claim
	// Now is the clock for timestamps the API writes; nil means time.Now.
	Now func() time.Time
	// Stream tunes the SSE endpoints; zero fields take defaults.
	Stream StreamConfig
	// LogTail serves the live part of execution logs. Nil means the stream
	// polls the store until the execution ends.
	LogTail LogTail
	// LogBlobs reads stored log chunks from the backend each chunk names. Nil
	// reads them from Data (the db backend only).
	LogBlobs LogReader
	// RunNotify wakes run event streams early. Nil means polling only.
	RunNotify RunNotifier
	// Advance enqueues run.advance in the same transaction as a trigger or a
	// cancel request. Nil means nothing is enqueued.
	Advance RunEnqueuer
	// Targets is where `targets` comes from when a run is created (the same
	// source run.advance uses, or the first Decide would freeze an empty list).
	// Nil means none.
	Targets TargetSource
	// AuthorizeSecretRefs makes spec validation reject literal secret refs the
	// job project may not use. On with the db secret backend, off with --dev-nodes.
	AuthorizeSecretRefs bool
	// Secrets serves the secrets endpoints (admin token required). Nil answers
	// them with 503: secrets are disabled without a KEK.
	Secrets SecretBackend
	// RequireToken refuses job, run and execution requests without a valid
	// Bearer token (ADR-0060). A token that is sent is checked either way.
	RequireToken bool
	// OIDC enables browser login and ends anonymous access (ADR-0063). Nil
	// leaves login off.
	OIDC *OIDC
	// Mounts maps extra mux patterns (for example "/agent/v1/connect") to
	// handlers owned by other components. They are wrapped by the same
	// request logging as the API.
	Mounts map[string]http.Handler
	Logger *slog.Logger
}

// TargetSource lists the nodes `targets` is built from.
type TargetSource interface {
	// Targets scans the enabled nodes a job of project can target, in name
	// order, hands each batch to pick (nil keeps all) and returns the picked
	// nodes, stopping once there are limit of them. An error fails the request
	// (nothing is created).
	Targets(ctx context.Context, project string, pick func([]map[string]any) []map[string]any, limit int) ([]map[string]any, error)
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
	d := newDeps(cfg)
	env, err := expr.NewRequestEnv(expr.Limits{})
	if err != nil { // a constant environment: only a broken CEL library fails here
		panic(fmt.Sprintf("building the webhook environment: %v", err))
	}
	newHuma(mux, d, env)
	mux.Handle(hookPrefix, newHookHandler(d, env))
	if d.oidc != nil {
		a := authHandlers{d: d, limit: newRateLimiter(loginRate, loginBurst)}
		mux.HandleFunc("GET /auth/login", a.login)
		mux.HandleFunc("GET /auth/callback", a.callback)
		mux.HandleFunc("POST /auth/logout", a.logout)
	}
	mux.HandleFunc("/auth/", notFound) // login is off, or the method is wrong: never the SPA
	mux.HandleFunc("/api/", notFound)
	mux.HandleFunc("/internal/", notFound) // served only on the internal listener (ADR-0040)
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
