package winrmtest

import (
	"encoding/pem"
	"net/http/httptest"
)

// certPEM is the server's self-signed certificate in PEM form.
func certPEM(ts *httptest.Server) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}))
}
