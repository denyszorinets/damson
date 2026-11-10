// Package tlsroots builds the certificate pool plugins verify servers with: the
// system roots plus the operator's extra CA certificates (agent.tls_ca_file,
// delivered as the plugin Config key "tls_ca_pem"). Verification can never be
// switched off through it. It uses only the standard library.
package tlsroots

import (
	"crypto/x509"
	"errors"
)

// ConfigKey is the ExecRequest.Config key holding the extra PEM certificates.
const ConfigKey = "tls_ca_pem"

// ErrBadPEM is returned when extraPEM holds no certificate.
var ErrBadPEM = errors.New("holds no valid PEM certificate")

// Pool returns the system roots with extraPEM appended. An empty extraPEM
// yields the system roots alone.
func Pool(extraPEM string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if extraPEM != "" && !pool.AppendCertsFromPEM([]byte(extraPEM)) {
		return nil, ErrBadPEM
	}
	return pool, nil
}
