package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

const requestIDHeader = "X-Request-Id"

type ctxKey struct{}

// loggerFrom returns the request-scoped logger set by logRequests.
func loggerFrom(r *http.Request) *slog.Logger {
	if l, ok := r.Context().Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer (flushing
// for SSE later).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// logRequests assigns a request id, stores a request-scoped logger in the
// context and logs one line per request. It logs the path only: never the
// query string, headers or body, which may carry credentials.
func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := newRequestID()
		w.Header().Set(requestIDHeader, id)
		rl := log.With("request_id", id)
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), ctxKey{}, rl)))
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		rl.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration_ms", time.Since(start).Milliseconds())
	})
}
