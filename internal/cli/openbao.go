package cli

import (
	"fmt"
	"os"

	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/secrets/openbao"
)

// openBaoConfig maps the configuration to the backend's, or returns nil when
// secrets.backend is db.
func openBaoConfig(cfg config.Config) (*openbao.Config, error) {
	if cfg.Secrets.Backend != config.SecretsOpenBao {
		return nil, nil
	}
	o := cfg.Secrets.OpenBao
	var ca []byte
	if o.TLSCAFile != "" {
		var err error
		if ca, err = os.ReadFile(o.TLSCAFile); err != nil {
			return nil, fmt.Errorf("secrets.openbao.tls_ca_file: %w", err)
		}
	}
	return &openbao.Config{
		Address: o.Address, Namespace: o.Namespace, Mount: o.Mount, Prefix: o.Prefix, ReadOnly: o.ReadOnly,
		CAPEM: ca, AllowPlainHTTP: o.AllowPlainHTTP, RequestTimeout: cfg.OpenBaoRequestTimeout(), CacheTTL: cfg.OpenBaoCacheTTL(),
		Auth: openbao.Auth{
			Method: o.Auth.Method, Token: o.Token, TokenFile: o.Auth.TokenFile,
			RoleID: o.Auth.RoleID, RoleIDFile: o.Auth.RoleIDFile, SecretID: o.SecretID, SecretIDFile: o.Auth.SecretIDFile,
			Role: o.Auth.Role, SATokenFP: o.Auth.ServiceAccountTokenFile, Mount: o.Auth.Mount,
		},
	}, nil
}
