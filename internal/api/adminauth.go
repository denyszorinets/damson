package api

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

// adminSecurity is the OpenAPI security requirement of admin operations.
var adminSecurity = []map[string][]string{{"bearerAuth": {}}}

// requireAdmin guards an operation with the admin bootstrap token, the interim
// form of the local recovery token until OIDC (ARCHITECTURE 12.2). It runs
// before the request body is parsed. The stored hash is read on every request,
// so creating or revoking the token takes effect at once. Failures say only
// that the token is missing or wrong.
func requireAdmin(api huma.API, d deps) func(ctx huma.Context, next func(huma.Context)) {
	deny := func(ctx huma.Context, msg string) {
		ctx.SetHeader("WWW-Authenticate", `Bearer realm="damson-admin"`)
		_ = huma.WriteErr(api, ctx, http.StatusUnauthorized, msg)
	}
	return func(ctx huma.Context, next func(huma.Context)) {
		if d.ds == nil {
			_ = huma.WriteErr(api, ctx, http.StatusServiceUnavailable, "a required dependency is not available")
			return
		}
		h := ctx.Header("Authorization")
		tok := ""
		if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") { // the scheme is case-insensitive (RFC 9110)
			tok = strings.TrimSpace(h[7:])
		}
		if !strings.HasPrefix(tok, tokens.AdminPrefix) {
			deny(ctx, "an admin token is required")
			return
		}
		s, err := d.ds.GetSetting(ctx.Context(), tokens.AdminSettingKey)
		switch {
		case errors.Is(err, store.ErrNotFound) || (err == nil && s.Value == ""):
			deny(ctx, "the admin token is not valid") // same text as a wrong token: no hint whether one is set
			return
		case err != nil:
			_ = huma.WriteErr(api, ctx, http.StatusServiceUnavailable, "a required dependency is not available")
			return
		}
		if subtle.ConstantTimeCompare([]byte(tokens.Hash(tok)), []byte(s.Value)) != 1 {
			deny(ctx, "the admin token is not valid")
			return
		}
		next(ctx)
	}
}
