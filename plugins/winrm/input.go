package winrm

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Transports and authentication schemes.
const (
	transportHTTPS = "https"
	transportHTTP  = "http"
	authNTLM       = "ntlm"
	authBasic      = "basic"
)

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Input is the winrm plugin input. Exactly one of Command and Powershell is set.
type Input struct {
	Node               string            `json:"node" jsonschema:"description=Name of the inventory node to run on (a node with transport winrm),minLength=1"`
	Command            string            `json:"command,omitempty" jsonschema:"description=Command line run with cmd.exe"`
	Powershell         string            `json:"powershell,omitempty" jsonschema:"description=PowerShell script run as a script block"`
	Env                map[string]string `json:"env,omitempty" jsonschema:"description=Environment variables for the command. A value of the form secret://... is resolved from the secret bundle. They travel to the node in the request body and never in the command line."`
	Credential         string            `json:"credential,omitempty" jsonschema:"description=Credential secret:// ref (fields username and password) that overrides the node credential"`
	Transport          string            `json:"transport,omitempty" jsonschema:"description=https (default) or http. http needs ntlm and the operator setting allow_insecure_winrm.,enum=https,enum=http,default=https"`
	Auth               string            `json:"auth,omitempty" jsonschema:"description=Authentication scheme: ntlm (default) or basic. basic is refused over http.,enum=ntlm,enum=basic,default=ntlm"`
	InsecureSkipVerify bool              `json:"insecure_skip_verify,omitempty" jsonschema:"description=Do not verify the server certificate. Needs the operator setting allow_insecure_winrm; prefer agent.tls_ca_file for a private CA."`
	SuccessExitCodes   []int             `json:"success_exit_codes,omitempty" jsonschema:"description=Exit codes that count as success,default=[0]"`
}

// Output is the winrm plugin output; it is the same as the ssh plugin's.
type Output struct {
	ExitCode   int            `json:"exit_code" jsonschema:"description=Exit code of the command; -1 when it did not exit normally"`
	StdoutTail string         `json:"stdout_tail" jsonschema:"description=Last 64 KiB of stdout without output-protocol lines"`
	StderrTail string         `json:"stderr_tail" jsonschema:"description=Last 64 KiB of stderr"`
	Values     map[string]any `json:"values,omitempty" jsonschema:"description=Values set with ##damson-output and ##damson-output-json lines"`
}

// decode parses and validates the input. Unknown fields are rejected. It does
// not look at the operator settings: those gate the connection (see
// checkSecurity), so Validate on the controller side stays config-free.
func decode(raw json.RawMessage) (Input, error) {
	var in Input
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, pluginsdk.ValidationError("invalid input: %v", err)
	}
	if in.Transport == "" {
		in.Transport = transportHTTPS
	}
	if in.Auth == "" {
		in.Auth = authNTLM
	}
	if len(in.SuccessExitCodes) == 0 {
		in.SuccessExitCodes = []int{0}
	}
	return in, in.validate()
}

func (in Input) validate() error {
	switch {
	case in.Node == "":
		return pluginsdk.ValidationError("node is required")
	case (in.Command == "") == (in.Powershell == ""):
		return pluginsdk.ValidationError("exactly one of command and powershell is required")
	case strings.ContainsRune(in.Command+in.Powershell, 0):
		return pluginsdk.ValidationError("command and powershell must not contain NUL bytes")
	case strings.ContainsAny(in.Command, "\r\n"):
		return pluginsdk.ValidationError("command must be a single line (cmd.exe runs only the first line); use powershell for a script")
	case in.Transport != transportHTTPS && in.Transport != transportHTTP:
		return pluginsdk.ValidationError("transport must be %q or %q", transportHTTPS, transportHTTP)
	case in.Auth != authNTLM && in.Auth != authBasic:
		return pluginsdk.ValidationError("auth must be %q or %q", authNTLM, authBasic)
	case in.Transport == transportHTTP && in.Auth == authBasic:
		return pluginsdk.ValidationError("basic authentication over http would send the password in clear text; use ntlm or https")
	case in.Transport == transportHTTP && in.InsecureSkipVerify:
		return pluginsdk.ValidationError("insecure_skip_verify has no meaning over http")
	}
	seen := map[string]string{}
	for k, v := range in.Env {
		if prev, dup := seen[strings.ToUpper(k)]; dup {
			return pluginsdk.ValidationError("env names %q and %q differ only in case; Windows treats them as one variable", prev, k)
		}
		seen[strings.ToUpper(k)] = k
		if !envNameRe.MatchString(k) {
			return pluginsdk.ValidationError("env name %q is not a valid variable name", k)
		}
		if strings.ContainsRune(v, 0) {
			return pluginsdk.ValidationError("env %s must not contain NUL bytes", k)
		}
	}
	for _, c := range in.SuccessExitCodes {
		if c < 0 {
			return pluginsdk.ValidationError("success_exit_codes entry %d must not be negative", c)
		}
	}
	return nil
}

func (in Input) successful(code int) bool {
	for _, c := range in.SuccessExitCodes {
		if c == code {
			return true
		}
	}
	return false
}
