package ssh

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

const defaultInterpreter = "/bin/sh"

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Input is the ssh plugin input. Exactly one of Command and Script is set.
type Input struct {
	Node             string            `json:"node" jsonschema:"description=Name of the inventory node to run on,minLength=1"`
	Command          string            `json:"command,omitempty" jsonschema:"description=Shell command line to run on the node"`
	Script           string            `json:"script,omitempty" jsonschema:"description=Script body; it is uploaded to a temporary file on the node and executed with the interpreter"`
	Interpreter      string            `json:"interpreter,omitempty" jsonschema:"description=Interpreter (with optional arguments) for command or script,default=/bin/sh"`
	Env              map[string]string `json:"env,omitempty" jsonschema:"description=Environment variables. A value of the form secret://... is resolved from the secret bundle."`
	Workdir          string            `json:"workdir,omitempty" jsonschema:"description=Remote working directory"`
	Sudo             bool              `json:"sudo,omitempty" jsonschema:"description=Run through sudo -n (passwordless sudo is required on the node)"`
	SudoUser         string            `json:"sudo_user,omitempty" jsonschema:"description=Target user for sudo; requires sudo"`
	Pty              bool              `json:"pty,omitempty" jsonschema:"description=Request a pseudo terminal. stderr is then merged into stdout and the command is passed as an argument instead of on stdin."`
	Credential       string            `json:"credential,omitempty" jsonschema:"description=Credential secret:// ref that overrides the node credential"`
	SuccessExitCodes []int             `json:"success_exit_codes,omitempty" jsonschema:"description=Exit codes that count as success,default=[0]"`
}

// Output is the ssh plugin output.
type Output struct {
	ExitCode   int            `json:"exit_code" jsonschema:"description=Exit code of the remote command; -1 when it did not exit normally"`
	StdoutTail string         `json:"stdout_tail" jsonschema:"description=Last 64 KiB of stdout without output-protocol lines"`
	StderrTail string         `json:"stderr_tail" jsonschema:"description=Last 64 KiB of stderr"`
	Values     map[string]any `json:"values,omitempty" jsonschema:"description=Values set with ##damson-output and ##damson-output-json lines"`
}

// decode parses and validates the input. Unknown fields are rejected.
func decode(raw json.RawMessage) (Input, error) {
	var in Input
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, pluginsdk.ValidationError("invalid input: %v", err)
	}
	if err := in.validate(); err != nil {
		return in, err
	}
	if in.Interpreter == "" {
		in.Interpreter = defaultInterpreter
	}
	if len(in.SuccessExitCodes) == 0 {
		in.SuccessExitCodes = []int{0}
	}
	return in, nil
}

func (in Input) validate() error {
	switch {
	case in.Node == "":
		return pluginsdk.ValidationError("node is required")
	case (in.Command == "") == (in.Script == ""):
		return pluginsdk.ValidationError("exactly one of command and script is required")
	case strings.ContainsRune(in.Command+in.Script, 0):
		return pluginsdk.ValidationError("command and script must not contain NUL bytes")
	case in.SudoUser != "" && !in.Sudo:
		return pluginsdk.ValidationError("sudo_user requires sudo")
	case hasControl(in.Interpreter) || hasControl(in.Workdir) || hasControl(in.SudoUser):
		return pluginsdk.ValidationError("interpreter, workdir and sudo_user must be single-line text")
	case in.SudoUser != "" && strings.HasPrefix(in.SudoUser, "-"):
		return pluginsdk.ValidationError("sudo_user must not start with '-'")
	}
	for k, v := range in.Env {
		if !envNameRe.MatchString(k) {
			return pluginsdk.ValidationError("env name %q is not a valid variable name", k)
		}
		if strings.ContainsRune(v, 0) {
			return pluginsdk.ValidationError("env %s must not contain NUL bytes", k)
		}
	}
	for _, c := range in.SuccessExitCodes {
		if c < 0 || c > 255 {
			return pluginsdk.ValidationError("success_exit_codes entry %d is outside 0..255", c)
		}
	}
	return nil
}

func hasControl(s string) bool {
	return strings.ContainsAny(s, "\x00\r\n")
}

func (in Input) successful(code int) bool {
	for _, c := range in.SuccessExitCodes {
		if c == code {
			return true
		}
	}
	return false
}
