//go:build winrmwindows

package winrm_test

// These tests run the plugin against a real WinRM service on a Windows host
// (the "WinRM on Windows" workflow, #163). They are not part of make test: they
// need DAMSON_WINRM_HOST, DAMSON_WINRM_PORT, DAMSON_WINRM_USER,
// DAMSON_WINRM_PASSWORD and DAMSON_WINRM_CA_FILE (the PEM of the host's
// self-signed https listener certificate).

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

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

// The first shell on a cold host can be slow (profile and assembly loading);
// this absorbs that, with its time reported, so the tests after it measure the plugin.
func TestRealWarmUp(t *testing.T) {
	mk := realFixture(t, "")
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	o := plugintest.Exec(ctx, t, plug(), mk(map[string]any{"powershell": "exit 0"}))
	t.Logf("first powershell step: %v (%v)", time.Since(start), o.Result.Error)
}

// The bootstrap runs on a real powershell.exe: UTF-8 output, stderr without
// CLIXML, and the exit code of an explicit exit.
func TestRealPowerShellOutputAndExitCode(t *testing.T) {
	mk := realFixture(t, "")
	script := "Write-Output 'héllo wörld'\n[Console]::Error.WriteLine('to-stderr')\nexit 7"
	o := realExec(t, mk(map[string]any{"powershell": script, "success_exit_codes": []int{7}}))
	out := requireOK(t, o)
	if out.ExitCode != 7 || !strings.Contains(out.StdoutTail, "héllo wörld") {
		t.Fatalf("output = %+v", out)
	}
	if !strings.Contains(out.StderrTail, "to-stderr") || strings.Contains(out.StderrTail, "CLIXML") {
		t.Fatalf("stderr = %q", out.StderrTail)
	}
	requireKind(t, realExec(t, mk(map[string]any{"powershell": "exit 3"})), pluginsdk.KindExitCode, "3")
	requireKind(t, realExec(t, mk(map[string]any{"powershell": "throw 'boom'"})), pluginsdk.KindExitCode, "1")
}

func TestRealCmdCommand(t *testing.T) {
	mk := realFixture(t, "")
	out := requireOK(t, realExec(t, mk(map[string]any{"command": "echo hi & exit /b 3", "success_exit_codes": []int{3}})))
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
	o := realExec(t, mk(map[string]any{"powershell": script, "env": map[string]string{"DAMSON_TOK": "secret://win/tok"}}))
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
	requireKind(t, realExec(t, mk(map[string]any{"powershell": "exit 0"})), pluginsdk.KindAuth, "")
}

// Basic over https: the service accepts it only because the workflow enables it.
func TestRealBasicAuthOverHTTPS(t *testing.T) {
	mk := realFixture(t, "")
	out := requireOK(t, realExec(t, mk(map[string]any{"auth": "basic", "powershell": "Write-Output 'basic-ok'"})))
	if !strings.Contains(out.StdoutTail, "basic-ok") {
		t.Fatalf("output = %+v", out)
	}
	wrong := realFixture(t, "definitely-not-the-password")
	requireKind(t, realExec(t, wrong(map[string]any{"auth": "basic", "powershell": "exit 0"})), pluginsdk.KindAuth, "")
}

// The http transport runs NTLM with message sealing (the service has
// AllowUnencrypted off, so a request that is not sealed is refused). The
// operator gate must be open for it.
func TestRealHTTPWithNTLMSealing(t *testing.T) {
	port, err := strconv.Atoi(os.Getenv("DAMSON_WINRM_HTTP_PORT"))
	if err != nil {
		t.Skip("DAMSON_WINRM_HTTP_PORT is not set")
	}
	mk := realFixture(t, "")
	build := func(body map[string]any, gate bool) plugintest.Case {
		body["transport"] = "http"
		c := mk(body)
		n := c.Nodes["win1"]
		n.Port = port
		c.Nodes["win1"] = n
		if gate {
			c.Config[winrm.ConfigAllowInsecureWinRM] = "true"
		}
		return c
	}
	requireKind(t, exec(t, build(map[string]any{"powershell": "exit 0"}, false)), pluginsdk.KindValidation, winrm.ConfigAllowInsecureWinRM)
	out := requireOK(t, realExec(t, build(map[string]any{"powershell": "Write-Output 'sealed-ok'"}, true)))
	if !strings.Contains(out.StdoutTail, "sealed-ok") {
		t.Fatalf("output = %+v", out)
	}
}

// Cancelling a running step returns promptly and ends the process on the host.
func TestRealCancelEndsTheRunningProcess(t *testing.T) {
	mk := realFixture(t, "")
	pidFile := `$env:TEMP\damson-cancel.pid`
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Second, cancel)
	start := time.Now()
	script := "$PID | Out-File -Encoding ascii " + pidFile + "\nStart-Sleep -Seconds 300"
	o := plugintest.Exec(ctx, t, plug(), mk(map[string]any{"powershell": script}))
	if d := time.Since(start); d > 40*time.Second {
		t.Fatalf("cancel took %s", d)
	}
	requireKind(t, o, pluginsdk.KindCancelled, "")
	check := "$p = [int](Get-Content " + pidFile + ")\nif (Get-Process -Id $p -ErrorAction SilentlyContinue) { exit 9 }"
	deadline := time.Now().Add(30 * time.Second)
	for {
		o := realExec(t, mk(map[string]any{"powershell": check}))
		if o.Result.Error == nil {
			return
		}
		if time.Now().After(deadline) {
			// Say whether the node still has the shell (the delete never arrived) or
			// has no shell but a live process.
			diag := realExec(t, mk(map[string]any{"powershell": "Get-WSManInstance -ResourceURI shell -Enumerate | ForEach-Object { 'shell ' + $_.ShellId + ' idle=' + $_.IdleTime + ' owner=' + $_.Owner }\nGet-Process -Id ([int](Get-Content " + pidFile + ")) | Format-List Id,StartTime,Name | Out-String"}))
			t.Fatalf("the process is still running on the host: %v\n%s", o.Result.Error, diag.Result.Outputs)
		}
		time.Sleep(2 * time.Second)
	}
}

// realExec is exec with a retry for the host-side "Class not registered" fault
// that a freshly enabled WinRM service on a CI runner sometimes returns for
// the first commands (seen once in the Windows job); anything else is final.
func realExec(t *testing.T, c plugintest.Case) plugintest.Outcome {
	t.Helper()
	var o plugintest.Outcome
	for attempt := 0; attempt < 4; attempt++ {
		o = exec(t, c)
		if o.Result.Error == nil || !strings.Contains(o.Result.Error.Message, "Class not registered") {
			return o
		}
		t.Logf("attempt %d: %v", attempt+1, o.Result.Error)
		time.Sleep(10 * time.Second)
	}
	return o
}
