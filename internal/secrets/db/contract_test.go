package db_test

import (
	"testing"

	"github.com/denyszorinets/damson/internal/secrets/secretstest"
)

func TestContract(t *testing.T) {
	secretstest.Run(t, func(t *testing.T) secretstest.Fixture {
		s := newStore(t)
		return secretstest.Fixture{Backend: newBackend(t, s, key(t)), Audit: s}
	})
}
