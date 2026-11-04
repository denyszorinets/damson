package api_test

import (
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// With AuthorizeSecretRefs a literal secret ref must be well formed and in the
// job's project scope; _inventory is not allowed as a literal. The issue names
// the path, never the ref.
func TestValidateAuthorizesLiteralSecretRefs(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		reg, err := pluginreg.New([]pluginsdk.Manifest{apiManifest("ssh", "1.0.0")}, nil)
		require.NoError(t, err)
		srv := api.NewServer(api.Config{
			Addr: "127.0.0.1:0", Store: st, Data: st, Plugins: reg, UI: ui(), AuthorizeSecretRefs: true,
			Now: func() time.Time { return t0 }, Logger: slog.New(slog.DiscardHandler),
		})
		f := &jrFixture{st: st, h: srv.Handler(), logs: &syncBuffer{}}
		for _, tc := range []struct {
			name, ref string
			ok        bool
		}{
			{"own project", "secret://default/key", true},
			{"own project with field", "secret://default/key#password", true},
			{"other project", "secret://other/key", false},
			{"inventory literal", "secret://_inventory/key", false},
			{"malformed", "secret://Default/Key", false},
			{"interpolated is left to dispatch", "secret://default/${{ inputs.h }}", true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var out struct {
					Valid  bool `json:"valid"`
					Errors []struct {
						Path    string `json:"path"`
						Message string `json:"message"`
					} `json:"errors"`
				}
				spec := specDoc("j", "", "ssh", `{"host":"`+tc.ref+`"}`)
				f.callOK(t, http.MethodPost, "/api/v1/jobs/validate", wrapSpec(spec), http.StatusOK, &out)
				assert.Equal(t, tc.ok, out.Valid)
				for _, e := range out.Errors {
					assert.NotContains(t, e.Message, tc.ref)
				}
			})
		}
		t.Run("option default", func(t *testing.T) {
			var out struct {
				Valid bool `json:"valid"`
			}
			opt := `"options":{"type":"object","properties":{"k":{"type":"string","x-damson-secret":true,"default":"secret://other/key"}}},`
			spec := strings.Replace(specDoc("j", "", "ssh", `{"host":"h"}`), `"steps"`, opt+`"steps"`, 1)
			f.callOK(t, http.MethodPost, "/api/v1/jobs/validate", wrapSpec(spec), http.StatusOK, &out)
			assert.False(t, out.Valid)
		})
	})
}
