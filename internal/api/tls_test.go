package api_test

import (
	"crypto/tls"
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/testutil/selfsigned"
	"github.com/denyszorinets/damson/internal/tlsreload"
)

func TestServerServesTLS(t *testing.T) {
	pair := selfsigned.New(t, "damson")
	certFile, keyFile := pair.Write(t, t.TempDir())
	log := slog.New(slog.NewJSONHandler(&syncBuffer{}, nil))
	rl, err := tlsreload.New(certFile, keyFile, log)
	require.NoError(t, err)
	srv := api.NewServer(api.Config{Addr: "127.0.0.1:0", Store: fakeStore{}, UI: ui(), Logger: log, TLS: rl.Config()})
	require.NoError(t, srv.Start(t.Context()))
	t.Cleanup(func() { _ = srv.Stop(t.Context()) })

	trusting := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pair.Pool(), MinVersion: tls.VersionTLS12}}}
	resp, err := trusting.Get("https://" + srv.Addr() + "/healthz")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotNil(t, resp.TLS)
	assert.GreaterOrEqual(t, int(resp.TLS.Version), tls.VersionTLS12)

	// A client that does not trust the certificate refuses it.
	_, err = (&http.Client{}).Get("https://" + srv.Addr() + "/healthz")
	assert.Error(t, err)
	// Plain HTTP to the TLS port does not get an API answer.
	plain, err := (&http.Client{}).Get("http://" + srv.Addr() + "/healthz")
	if err == nil {
		defer func() { _ = plain.Body.Close() }()
		assert.NotEqual(t, http.StatusOK, plain.StatusCode)
	}
}
