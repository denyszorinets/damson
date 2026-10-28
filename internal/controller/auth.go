package controller

import (
	"errors"
	"net/http"
	"strings"
	"unicode"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

const (
	agentIDHeader = "X-Damson-Agent-Id"
	bearerPrefix  = "Bearer "
)

// identity is what the upgrade request proved.
type identity struct {
	agentID string
	pool    store.Pool
	tokenID string
}

// secureTransport reports whether the request arrived over TLS, directly or
// through a TLS-terminating proxy that says so.
func secureTransport(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// authenticate validates the request before any upgrade. On failure it has
// already written the problem+json response and returns false. The token is
// only ever hashed; it is never logged or stored.
func (g *Gateway) authenticate(w http.ResponseWriter, r *http.Request) (identity, bool) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		api.WriteProblem(w, r, http.StatusMethodNotAllowed, "urn:damson:problem:method-not-allowed", "use GET with a WebSocket upgrade")
		return identity{}, false
	}
	if !g.cfg.InsecureDev && !secureTransport(r) {
		api.WriteProblem(w, r, http.StatusForbidden, api.TypeForbidden,
			"plain ws:// is not allowed; connect with wss:// (or run the controller with insecure_dev for development)")
		return identity{}, false
	}
	token, ok := bearerToken(r)
	if !ok {
		unauthorized(w, r, "a pool token is required: Authorization: Bearer <token>")
		return identity{}, false
	}
	agentID := r.Header.Get(agentIDHeader)
	if !validAgentID(agentID) {
		api.WriteProblem(w, r, http.StatusBadRequest, api.TypeBadRequest, agentIDHeader+" header is missing or invalid")
		return identity{}, false
	}
	pool, tok, err := g.cfg.Store.LookupPoolToken(r.Context(), tokens.Hash(token))
	switch {
	case errors.Is(err, store.ErrNotFound):
		unauthorized(w, r, "the pool token is unknown or revoked")
		return identity{}, false
	case err != nil:
		api.WriteError(w, r, err)
		return identity{}, false
	}
	return identity{agentID: agentID, pool: pool, tokenID: tok.ID}, true
}

func unauthorized(w http.ResponseWriter, r *http.Request, detail string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	api.WriteProblem(w, r, http.StatusUnauthorized, api.TypeUnauthorized, detail)
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) <= len(bearerPrefix) || !strings.EqualFold(h[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}
	t := strings.TrimSpace(h[len(bearerPrefix):])
	return t, t != ""
}

func validAgentID(id string) bool {
	if id == "" || len(id) > maxAgentIDLen {
		return false
	}
	for _, c := range id {
		if !unicode.IsPrint(c) || unicode.IsSpace(c) {
			return false
		}
	}
	return true
}
