package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Checker is what the readiness probe needs from the store.
type Checker interface {
	Ping(ctx context.Context) error
	MigrationVersion(ctx context.Context) (int64, error)
}

const readyTimeout = 3 * time.Second

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// healthz is liveness: the process is up and serving. It touches nothing else.
func healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, r, "GET, HEAD")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz is readiness: the database answers and migrations have been applied.
// Failures are 503 problem+json; the cause is logged, not returned.
func readyz(c Checker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, r, "GET, HEAD")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()
		if err := checkReady(ctx, c); err != nil {
			WriteError(w, r, fmt.Errorf("%w: %w", errUnavailable, err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}
}

func checkReady(ctx context.Context, c Checker) error {
	if err := c.Ping(ctx); err != nil {
		return fmt.Errorf("database ping: %w", err)
	}
	v, err := c.MigrationVersion(ctx)
	if err != nil {
		return fmt.Errorf("migration version: %w", err)
	}
	if v == 0 {
		return errors.New("no migrations applied")
	}
	return nil
}
