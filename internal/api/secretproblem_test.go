package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/denyszorinets/damson/internal/secrets"
)

func TestSecretProblemExternalBackendErrors(t *testing.T) {
	tests := []struct {
		err    error
		status int
	}{
		{fmt.Errorf("put: %w", secrets.ErrReadOnly), http.StatusConflict},
		{fmt.Errorf("get: %w", secrets.ErrUnavailable), http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		p, ok := secretProblem(tc.err)
		if !ok || p.Status != tc.status {
			t.Fatalf("%v -> %+v ok=%v; want status %d", tc.err, p, ok, tc.status)
		}
	}
}
