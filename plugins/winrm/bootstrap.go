package winrm

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"unicode/utf16"
)

// bootstrapScript is the only thing on the command line. It reads one base64
// blob (UTF-8 JSON {"env":{...},"command":"..."} or {"env":{...},"powershell":"..."})
// from stdin, sets the environment and runs the payload. The payload and the
// environment values are data, so nothing needs quoting, and neither the user's
// script nor a secret value appears in the process command line, in the process
// creation event (4688) or in a length limit of the command line.
//
// Behavior: an explicit "exit N" in the script gives N; a terminating error
// writes its text to stderr and gives 1; otherwise a non-zero $LASTEXITCODE of
// the last native command is the exit code, else 0. A command line payload runs
// under cmd.exe (/d /s /c) with inherited handles and its exit code is passed on.
// Progress records are silenced and errors are plain text, so no CLIXML reaches
// the log.
const bootstrapScript = `$ProgressPreference = 'SilentlyContinue'
try { [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false } catch {}
try {
  $raw = [Console]::In.ReadToEnd().Trim()
  $req = [System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($raw)) | ConvertFrom-Json
  if ($req.env) {
    foreach ($p in $req.env.PSObject.Properties) {
      [Environment]::SetEnvironmentVariable($p.Name, [string]$p.Value, 'Process')
    }
  }
  if ($null -ne $req.command) {
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $env:ComSpec
    $psi.Arguments = '/d /s /c "' + $req.command + '"'
    $psi.UseShellExecute = $false
    $proc = [System.Diagnostics.Process]::Start($psi)
    $proc.WaitForExit()
    exit $proc.ExitCode
  }
  $global:LASTEXITCODE = 0
  & ([scriptblock]::Create($req.powershell))
  if ($LASTEXITCODE) { exit $LASTEXITCODE }
  exit 0
} catch {
  [Console]::Error.WriteLine(($_ | Out-String).TrimEnd())
  exit 1
}`

// powershellArgs are the flags before -EncodedCommand. -ExecutionPolicy is left
// out: it does not apply to an encoded command or to a script block.
const powershellArgs = "powershell.exe -NoLogo -NoProfile -NonInteractive -OutputFormat Text -EncodedCommand "

// bootstrapCommand is the command line sent to the node: the fixed bootstrap,
// UTF-16LE and base64 encoded as -EncodedCommand expects.
func bootstrapCommand() string {
	return powershellArgs + encodeUTF16LE(bootstrapScript)
}

func encodeUTF16LE(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, 2*len(u))
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}

// request is the JSON the bootstrap reads. Exactly one of Command and
// Powershell is set; the other is omitted so PowerShell sees $null.
type request struct {
	Env        map[string]string `json:"env,omitempty"`
	Command    *string           `json:"command,omitempty"`
	Powershell *string           `json:"powershell,omitempty"`
}

// stdinPayload is what is written to the bootstrap's stdin: base64 of the
// request JSON. Base64 keeps the transfer independent of the console code page.
func stdinPayload(in Input, env map[string]string) ([]byte, error) {
	r := request{Env: env}
	switch {
	case in.Command != "":
		r.Command = &in.Command
	case in.Powershell != "":
		r.Powershell = &in.Powershell
	default:
		return nil, errors.New("no command or powershell")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, errors.New("encoding the request failed")
	}
	return []byte(base64.StdEncoding.EncodeToString(raw) + "\n"), nil
}
