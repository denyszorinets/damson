package winrm

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	mwinrm "github.com/masterzen/winrm"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/internal/tlsroots"
)

// ConfigAllowInsecureWinRM is the ExecRequest.Config key that lets a step use
// transport "http" or insecure_skip_verify. Its value must be "true". Both
// weaken the protection of the connection, so the operator opts in per agent;
// for a private CA use the tls_ca_file setting instead of skipping verification.
const ConfigAllowInsecureWinRM = "allow_insecure_winrm"

// ConfigTLSCAPEM is the Config key with extra PEM CA certificates trusted for
// https, in addition to the system roots (agent.tls_ca_file).
const ConfigTLSCAPEM = tlsroots.ConfigKey

const (
	defaultHTTPSPort = 5986
	defaultHTTPPort  = 5985

	// receiveTimeout is how long one Receive long-poll may wait on the node. It
	// bounds how long a cancel can go unnoticed by the library, which checks the
	// context between polls; see cancel in run.go.
	receiveTimeout = "PT5S"
	// httpTimeout bounds one HTTP exchange and must exceed receiveTimeout.
	httpTimeout = 30 * time.Second
)

// checkSecurity applies the operator gate to what the step asked for.
func checkSecurity(req pluginsdk.ExecRequest, in Input) error {
	allowed := req.Config[ConfigAllowInsecureWinRM] == "true"
	switch {
	case in.Transport == transportHTTP && !allowed:
		return pluginsdk.ValidationError("transport http is not enabled on this agent (%s); use https", ConfigAllowInsecureWinRM)
	case in.InsecureSkipVerify && !allowed:
		return pluginsdk.ValidationError("insecure_skip_verify is not enabled on this agent (%s); put the CA in agent.tls_ca_file instead", ConfigAllowInsecureWinRM)
	}
	return nil
}

// credentials resolves username and password from the credential ref (the
// step's override, else the node's). NTLM accepts DOMAIN\user and user@domain.
func credentials(req pluginsdk.ExecRequest, node pluginsdk.NodeInfo, override string) (user, pass string, err error) {
	ref := override
	if ref == "" {
		ref = node.Credential
	}
	if ref == "" {
		return "", "", pluginsdk.NewError(pluginsdk.KindAuth, fmt.Sprintf("node %q has no credential", node.Name), nil)
	}
	if req.Secrets == nil {
		return "", "", pluginsdk.NewError(pluginsdk.KindAuth, "no secret resolver available", nil)
	}
	fields, rerr := req.Secrets.ResolveFields(ref)
	if rerr != nil {
		return "", "", pluginsdk.NewError(pluginsdk.KindAuth, "resolving credential "+ref+" failed", rerr)
	}
	user = fields["username"]
	if user == "" {
		user, _ = node.Attributes["username"].(string)
	}
	if user == "" || fields["password"] == "" {
		return "", "", pluginsdk.NewError(pluginsdk.KindAuth, "a winrm credential needs a username and a password", nil)
	}
	return user, fields["password"], nil
}

// newClient builds the library client for a node and a function that releases
// the connections when the step ends. The port is the node's own (an http node
// stores 5985).
//
// https uses this plugin's transport (see httpsTransport). http is only for
// NTLM with message encryption (sealing), which the server requires there; it
// uses the library's transport, whose idle connection cannot be closed from
// here, so the node ends it when its idle timeout passes.
func newClient(req pluginsdk.ExecRequest, node pluginsdk.NodeInfo, in Input, user, pass string) (*mwinrm.Client, func(), error) {
	port := node.Port
	if port == 0 {
		port = defaultHTTPSPort
		if in.Transport == transportHTTP {
			port = defaultHTTPPort
		}
	}
	ep := &mwinrm.Endpoint{Host: node.Address, Port: port, HTTPS: in.Transport == transportHTTPS, Timeout: httpTimeout}
	params := mwinrm.NewParameters(receiveTimeout, "en-US", 153600)
	release := func() {}

	if ep.HTTPS {
		url := fmt.Sprintf("https://%s/wsman", net.JoinHostPort(node.Address, strconv.Itoa(port)))
		ht, err := newHTTPSTransport(url, user, pass, node.Address, req.Config[ConfigTLSCAPEM], in.Auth == authNTLM, in.InsecureSkipVerify)
		if err != nil {
			return nil, nil, pluginsdk.ValidationError("%s %v", ConfigTLSCAPEM, err)
		}
		params.TransportDecorator = func() mwinrm.Transporter { return ht }
		release = ht.close
	} else {
		// The sealed body carries env values, so insist on a strong NTLM key.
		enc, err := mwinrm.NewEncryptionWithSettings("ntlm", &mwinrm.Settings{
			NTLMKeyExchangeOptions: mwinrm.NTLMKeyExchangeOptions{MinimumKeyBits: 128},
		})
		if err != nil {
			return nil, nil, pluginsdk.NewError(pluginsdk.KindInternal, "preparing ntlm failed", err)
		}
		params.TransportDecorator = func() mwinrm.Transporter { return enc }
	}
	c, err := mwinrm.NewClientWithParameters(ep, user, pass, params)
	if err != nil {
		return nil, nil, pluginsdk.NewError(pluginsdk.KindConnection, "preparing the winrm client for "+node.Name+" failed", err)
	}
	return c, release, nil
}

// classify maps a library error to a step error. The library wraps HTTP
// problems in plain errors, so this works on the text. The text never carries
// the password.
func classify(err error, host string) error {
	s := err.Error()
	switch {
	case strings.Contains(s, "401"), strings.Contains(s, "Unauthorized"), strings.Contains(s, "AccessDenied"), strings.Contains(s, "access is denied"):
		return pluginsdk.NewError(pluginsdk.KindAuth, "authentication to "+host+" failed", err)
	case strings.Contains(s, "certificate"), strings.Contains(s, "x509"):
		return pluginsdk.NewError(pluginsdk.KindConnection, "the certificate of "+host+" could not be verified: "+s, err)
	}
	return pluginsdk.NewError(pluginsdk.KindConnection, "winrm request to "+host+" failed: "+faultSummary(s), err)
}

var faultTextRe = regexp.MustCompile(`(?s)<(?:\w+:)?(?:Text|Message)(?:\s[^>]*)?>(.*?)</(?:\w+:)?(?:Text|Message)>`)

// faultSummary reduces a SOAP fault in an error text to its reason and message
// elements; other text is returned as it is, bounded.
func faultSummary(s string) string {
	const limit = 4000
	if strings.Contains(s, "Envelope") {
		var parts []string
		for _, m := range faultTextRe.FindAllStringSubmatch(s, -1) {
			if t := strings.TrimSpace(m[1]); t != "" && !slices.Contains(parts, t) {
				parts = append(parts, t)
			}
		}
		if len(parts) > 0 {
			s = "soap fault: " + strings.Join(parts, "; ")
		}
	}
	if len(s) > limit {
		s = s[:limit] + "..."
	}
	return s
}
