package node_test

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/controller/node"
	"github.com/denyszorinets/damson/internal/secrets"
	secretsdb "github.com/denyszorinets/damson/internal/secrets/db"
)

// A store failure is neither of the permanent errors, so the dispatcher
// requeues instead of failing the execution.
func TestSecretSourceStoreErrorIsTransient(t *testing.T) {
	st := migratedStore(t)
	ring, err := secrets.NewKeyring(kek(), "")
	if err != nil {
		t.Fatal(err)
	}
	src := node.NewSecretSource(secretsdb.New(st, ring, rand.Reader, time.Now, uuid.NewString), slog.Default())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = src.Secret(context.Background(), "run", "secret://p/x")
	if err == nil || errors.Is(err, dispatch.ErrUnknownSecret) || errors.Is(err, dispatch.ErrSecretUnavailable) {
		t.Fatalf("err = %v; want a transient error", err)
	}
}
