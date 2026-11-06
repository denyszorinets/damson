package winrm

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Azure/go-ntlmssp"
	mwinrm "github.com/masterzen/winrm"
	"github.com/masterzen/winrm/soap"

	"github.com/denyszorinets/damson/plugins/internal/tlsroots"
)

const soapContentType = "application/soap+xml"

// maxResponse bounds a WinRM response body held in memory.
const maxResponse = 16 << 20

// httpsTransport sends the WS-Man requests over https with a transport this
// plugin owns, instead of the library's. That gives three things the library's
// does not: the server certificate is verified against the system roots plus the
// operator's CA (the library would trust only the extra CA), no proxy is taken
// from the environment, and the idle connections can be closed when the step
// ends, so a step does not leave sockets and goroutines behind.
//
// NTLM goes through go-ntlmssp's Negotiator, which authenticates on the
// connection it uses; TLS already protects the payload, so no message
// encryption is needed. Basic sends the credentials with every request.
type httpsTransport struct {
	url, user, pass string
	tr              *http.Transport
	rt              http.RoundTripper
}

var _ mwinrm.Transporter = (*httpsTransport)(nil)

func newHTTPSTransport(url, user, pass, host, caPEM string, ntlm, skipVerify bool) (*httpsTransport, error) {
	pool, err := tlsroots.Pool(caPEM)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		Proxy:       nil,
		DialContext: (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    pool,
			ServerName: host,
			// Only set when the operator enabled allow_insecure_winrm (checked
			// in checkSecurity before the client is built).
			InsecureSkipVerify: skipVerify, //nolint:gosec // operator opt-in
		},
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: httpTimeout,
		// NTLM authenticates a connection, so every request of a step must use
		// the same one: the Receive long-poll, the stdin Send and a cancel
		// Signal then queue behind each other (as in the library's own NTLM
		// transport).
		MaxConnsPerHost:     1,
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     time.Minute,
	}
	h := &httpsTransport{url: url, user: user, pass: pass, tr: tr, rt: tr}
	if ntlm {
		h.rt = ntlmssp.Negotiator{RoundTripper: tr}
	}
	return h, nil
}

// Transport is part of mwinrm.Transporter; the transport is built up front.
func (*httpsTransport) Transport(*mwinrm.Endpoint) error { return nil }

// Post is part of mwinrm.Transporter.
func (h *httpsTransport) Post(_ *mwinrm.Client, request *soap.SoapMessage) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout+10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, strings.NewReader(withControlTimeout(request.String())))
	if err != nil {
		return "", fmt.Errorf("creating the request: %w", err)
	}
	req.Header.Set("Content-Type", soapContentType+";charset=UTF-8")
	req.SetBasicAuth(h.user, h.pass)
	resp, err := h.rt.RoundTrip(req)
	if err != nil {
		return "", fmt.Errorf("winrm request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return "", fmt.Errorf("reading the response (status %d): %w", resp.StatusCode, err)
	}
	if len(body) > maxResponse {
		return "", fmt.Errorf("the response exceeds %d bytes", maxResponse)
	}
	if resp.StatusCode != http.StatusOK {
		// A SOAP fault travels in a non-200 response; keep it, it says why.
		return "", fmt.Errorf("http error %d: %s", resp.StatusCode, faultSummary(string(body)))
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), soapContentType) {
		return "", fmt.Errorf("unexpected content type in the response (status %d)", resp.StatusCode)
	}
	return string(body), nil
}

// close drops the idle connections.
func (h *httpsTransport) close() { h.tr.CloseIdleConnections() }

// controlTimeout is the OperationTimeout of every request except Receive. The
// client has one timeout for all of them, and the short one that keeps the
// Receive long-poll (and so a cancel) responsive is too short for creating a
// shell or starting a command, which can take over 5 s on a cold Windows host.
// It must stay below httpTimeout.
const controlTimeout = "PT25S"

// withControlTimeout gives every request but Receive the control timeout.
func withControlTimeout(body string) string {
	if strings.Contains(body, "/shell/Receive") {
		return body
	}
	return strings.Replace(body, ">"+receiveTimeout+"<", ">"+controlTimeout+"<", 1)
}
