//go:build winrmwindows

package winrm_test

// These tests run the plugin against a real WinRM service on a Windows host
// (the "WinRM on Windows" workflow, #163). They are not part of make test: they
// need DAMSON_WINRM_HOST, DAMSON_WINRM_PORT, DAMSON_WINRM_USER,
// DAMSON_WINRM_PASSWORD and DAMSON_WINRM_CA_FILE (the PEM of the host's
// self-signed https listener certificate).

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
	"github.com/denyszorinets/damson/plugins/winrm"
)

const winSecret = "tok-WINDOWS-secret-4711"

func realFixture(t *testing.T, passwordOverride string) func(body map[string]any) plugintest.Case {
	t.Helper()
	get := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			t.Skipf("%s is not set", k)
		}
		return v
	}
	port, err := strconv.Atoi(get("DAMSON_WINRM_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	pem, err := os.ReadFile(get("DAMSON_WINRM_CA_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	pw := get("DAMSON_WINRM_PASSWORD")
	if passwordOverride != "" {
		pw = passwordOverride
	}
	node := pluginsdk.NodeInfo{Name: "win1", Address: get("DAMSON_WINRM_HOST"), Port: port, Transport: "winrm", OS: "windows", Credential: credRef}
	user := get("DAMSON_WINRM_USER")
	return func(body map[string]any) plugintest.Case {
		m := map[string]any{"node": "win1", "auth": "ntlm"}
		for k, v := range body {
			m[k] = v
		}
		raw, _ := json.Marshal(m)
		return plugintest.Case{
			Name: "real", Input: raw, Config: map[string]string{winrm.ConfigTLSCAPEM: string(pem)},
			Nodes: map[string]pluginsdk.NodeInfo{"win1": node},
			Bundle: map[string]map[string]string{
				credRef:            {"username": user, "password": pw},
				"secret://win/tok": {"value": winSecret},
			},
		}
	}
}

// The bootstrap runs on a real powershell.exe: UTF-8 output, stderr without
// CLIXML, and the exit code of an explicit exit.
func TestRealPowerShellOutputAndExitCode(t *testing.T) {
	mk := realFixture(t, "")
	script := "Write-Output 'héllo wörld'\n[Console]::Error.WriteLine('to-stderr')\nexit 7"
	o := exec(t, mk(map[string]any{"powershell": script, "success_exit_codes": []int{7}}))
	out := requireOK(t, o)
	if out.ExitCode != 7 || !strings.Contains(out.StdoutTail, "héllo wörld") {
		t.Fatalf("output = %+v", out)
	}
	if !strings.Contains(out.StderrTail, "to-stderr") || strings.Contains(out.StderrTail, "CLIXML") {
		t.Fatalf("stderr = %q", out.StderrTail)
	}
	requireKind(t, exec(t, mk(map[string]any{"powershell": "exit 3"})), pluginsdk.KindExitCode, "3")
	requireKind(t, exec(t, mk(map[string]any{"powershell": "throw 'boom'"})), pluginsdk.KindExitCode, "1")
}

func TestRealCmdCommand(t *testing.T) {
	mk := realFixture(t, "")
	out := requireOK(t, exec(t, mk(map[string]any{"command": "echo hi & exit /b 3", "success_exit_codes": []int{3}})))
	if out.ExitCode != 3 || !strings.Contains(out.StdoutTail, "hi") {
		t.Fatalf("output = %+v", out)
	}
}

// The payload travels on stdin: the secret env value reaches the script but
// not the process command line, and the plugin never logs it.
func TestRealSecretEnvIsNotOnTheCommandLine(t *testing.T) {
	mk := realFixture(t, "")
	script := "Write-Output ('len=' + $env:DAMSON_TOK.Length)\n" +
		"Write-Output ('cmdline=' + (Get-CimInstance Win32_Process -Filter \"ProcessId=$PID\").CommandLine)"
	o := exec(t, mk(map[string]any{"powershell": script, "env": map[string]string{"DAMSON_TOK": "secret://win/tok"}}))
	out := requireOK(t, o)
	if !strings.Contains(out.StdoutTail, "len="+strconv.Itoa(len(winSecret))) {
		t.Fatalf("the env value did not arrive: %q", out.StdoutTail)
	}
	if strings.Contains(out.StdoutTail, winSecret) || !strings.Contains(out.StdoutTail, "-EncodedCommand") {
		t.Fatalf("command line = %q", out.StdoutTail)
	}
}

func TestRealWrongPasswordIsAnAuthError(t *testing.T) {
	mk := realFixture(t, "definitely-not-the-password")
	requireKind(t, exec(t, mk(map[string]any{"powershell": "exit 0"})), pluginsdk.KindAuth, "")
}
