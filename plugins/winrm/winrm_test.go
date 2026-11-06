package winrm_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
	"github.com/denyszorinets/damson/plugins/internal/winrmtest"
	"github.com/denyszorinets/damson/plugins/winrm"
)

const (
	user     = `CORP\svc-damson`
	password = "winrm-PASSWORD-do-not-leak"
	credRef  = "secret://win/cred"
)

func plug() pluginsdk.Plugin { return winrm.Plugins()[0] }

type fixture struct {
	srv  *winrmtest.Server
	node pluginsdk.NodeInfo
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	srv := winrmtest.Start(t, winrmtest.Options{User: user, Password: password})
	return &fixture{srv: srv, node: pluginsdk.NodeInfo{
		Name: "win1", Address: srv.Host(), Port: srv.Port(), Transport: "winrm", OS: "windows", Credential: credRef,
	}}
}

// input builds a winrm input; extra keys override.
func input(body map[string]any) string {
	m := map[string]any{"node": "win1", "auth": "basic"}
	for k, v := range body {
		m[k] = v
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

func (f *fixture) caseOf(in string, cfg map[string]string) plugintest.Case {
	return plugintest.Case{
		Name: "c", Input: json.RawMessage(in), Config: cfg,
		Nodes:  map[string]pluginsdk.NodeInfo{"win1": f.node},
		Bundle: map[string]map[string]string{credRef: {"username": user, "password": password}},
	}
}

// trusted is the config that makes the fake server's certificate valid.
func (f *fixture) trusted() map[string]string {
	return map[string]string{winrm.ConfigTLSCAPEM: f.srv.CAPEM}
}

func exec(t *testing.T, c plugintest.Case) plugintest.Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return plugintest.Exec(ctx, t, plug(), c)
}

func decodeOut(t *testing.T, o plugintest.Outcome) winrm.Output {
	t.Helper()
	var out winrm.Output
	if err := json.Unmarshal(o.Result.Outputs, &out); err != nil {
		t.Fatalf("outputs %s: %v", o.Result.Outputs, err)
	}
	return out
}

func requireOK(t *testing.T, o plugintest.Outcome) winrm.Output {
	t.Helper()
	if o.Err != nil || o.Result.Error != nil {
		t.Fatalf("unexpected failure: err=%v step=%v", o.Err, o.Result.Error)
	}
	return decodeOut(t, o)
}

func requireKind(t *testing.T, o plugintest.Outcome, kind pluginsdk.ErrorKind, contains string) {
	t.Helper()
	if o.Err != nil {
		t.Fatalf("unexpected plugin fault: %v", o.Err)
	}
	e := o.Result.Error
	if e == nil || e.Kind != kind || !strings.Contains(e.Message, contains) {
		t.Fatalf("step error = %v; want %s containing %q", e, kind, contains)
	}
}

func TestContract(t *testing.T) {
	f := newFixture(t)
	blocking := f.caseOf(input(map[string]any{"command": "block"}), f.trusted())
	plugintest.RunContract(t, plug(), plugintest.Options{
		Cases: []plugintest.Case{
			f.caseOf(input(map[string]any{"command": "echo hello"}), f.trusted()),
			f.caseOf(input(map[string]any{"powershell": "echo from ps\nenv TOKEN", "env": map[string]string{"TOKEN": "x"}}), f.trusted()),
		},
		Blocking: &blocking,
	})
}

func TestSource(t *testing.T) { plugintest.CheckSource(t, ".") }

func TestOutputProtocolAndTails(t *testing.T) {
	f := newFixture(t)
	script := "echo first\necho ##damson-output who=svc\necho ##damson-output-json {\"n\":1}\nerr oops\necho last\nexit 0"
	o := exec(t, f.caseOf(input(map[string]any{"powershell": script}), f.trusted()))
	out := requireOK(t, o)
	if out.ExitCode != 0 || out.StdoutTail != "first\nlast\n" || out.StderrTail != "oops\n" || out.Values["who"] != "svc" || out.Values["n"] != float64(1) {
		t.Fatalf("output = %+v", out)
	}
	var logged []string
	for _, l := range o.Emitter.Lines() {
		logged = append(logged, string(l.Stream)+":"+l.Line)
	}
	if strings.Contains(strings.Join(logged, "|"), "##damson-output") {
		t.Fatalf("protocol lines reached the log: %v", logged)
	}
}

func TestExitCodes(t *testing.T) {
	f := newFixture(t)
	o := exec(t, f.caseOf(input(map[string]any{"powershell": "err boom\nexit 7"}), f.trusted()))
	requireKind(t, o, pluginsdk.KindExitCode, "code 7")
	if e := o.Result.Error; e.ExitCode == nil || *e.ExitCode != 7 || decodeOut(t, o).StderrTail != "boom\n" {
		t.Fatalf("error = %+v output = %+v", e, decodeOut(t, o))
	}
	requireOK(t, exec(t, f.caseOf(input(map[string]any{"command": "exit 3", "success_exit_codes": []int{0, 3}}), f.trusted())))
}

// The command line holds only the fixed bootstrap; the script, the environment
// and secret values travel in the request body.
func TestPayloadStaysOutOfTheCommandLine(t *testing.T) {
	f := newFixture(t)
	secret := "env-SECRET-VALUE-9f2"
	c := f.caseOf(input(map[string]any{
		"powershell": "echo marker-script-text\nenv API_TOKEN",
		"env":        map[string]string{"API_TOKEN": "secret://win/token", "PLAIN": "visible"},
	}), f.trusted())
	c.Secrets = map[string]string{"secret://win/token": secret}
	o := exec(t, c)
	out := requireOK(t, o)
	if !strings.Contains(out.StdoutTail, "marker-script-text") {
		t.Fatalf("stdout = %q", out.StdoutTail)
	}
	cmds := f.srv.Commands()
	if len(cmds) != 1 || !strings.HasPrefix(cmds[0], "powershell.exe -NoLogo -NoProfile -NonInteractive -OutputFormat Text -EncodedCommand ") {
		t.Fatalf("commands = %v", cmds)
	}
	for _, leak := range []string{secret, "marker-script-text", "visible", "API_TOKEN"} {
		if strings.Contains(cmds[0], leak) {
			t.Fatalf("%q reached the command line", leak)
		}
	}
	// The encoded part is the fixed bootstrap, so its decoding holds none either.
	enc := cmds[0][strings.LastIndex(cmds[0], " ")+1:]
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw)%2 != 0 {
		t.Fatalf("encoded command is not UTF-16LE base64: %v", err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	script := string(utf16.Decode(u))
	if !strings.Contains(script, "[Console]::In.ReadToEnd()") || strings.Contains(script, secret) {
		t.Fatalf("unexpected bootstrap: %.80s", script)
	}
	pl := f.srv.Payloads()
	if len(pl) != 1 || pl[0].Env["API_TOKEN"] != secret || pl[0].Env["PLAIN"] != "visible" || pl[0].Powershell == nil {
		t.Fatalf("payload = %+v", pl)
	}
	if strings.Contains(out.StdoutTail+o.LogOutput, secret) == false {
		// The fake echoes the value; the agent masks it, the plugin tests do not.
		t.Log("(the fake echoed the secret into stdout, as designed; masking is the agent's job)")
	}
}

func TestAuth(t *testing.T) {
	f := newFixture(t)
	c := f.caseOf(input(map[string]any{"command": "echo hi"}), f.trusted())
	c.Bundle[credRef] = map[string]string{"username": user, "password": "wrong-PASSWORD-guess"}
	o := exec(t, c)
	requireKind(t, o, pluginsdk.KindAuth, "authentication")
	all := o.LogOutput + o.Result.Error.Message
	for _, l := range o.Emitter.Lines() {
		all += l.Line
	}
	if strings.Contains(all, "wrong-PASSWORD-guess") || strings.Contains(all, password) {
		t.Fatal("a password reached the logs or the error")
	}

	c = f.caseOf(input(map[string]any{"command": "echo hi"}), f.trusted())
	c.Bundle[credRef] = map[string]string{"username": user}
	requireKind(t, exec(t, c), pluginsdk.KindAuth, "username and a password")

	noCred := f.node
	noCred.Credential = ""
	c = f.caseOf(input(map[string]any{"command": "echo hi"}), f.trusted())
	c.Nodes = map[string]pluginsdk.NodeInfo{"win1": noCred}
	requireKind(t, exec(t, c), pluginsdk.KindAuth, "no credential")

	// A credential override on the step wins over the node's.
	c = f.caseOf(input(map[string]any{"command": "echo hi", "credential": "secret://win/other"}), f.trusted())
	c.Bundle["secret://win/other"] = map[string]string{"username": user, "password": password}
	c.Bundle[credRef] = map[string]string{"username": "nobody", "password": "nope"}
	requireOK(t, exec(t, c))
}

// The server certificate is always verified: an unknown CA is refused, the
// operator's CA makes it work, and skipping verification is an operator opt-in.
func TestCertificateVerification(t *testing.T) {
	f := newFixture(t)
	in := input(map[string]any{"command": "echo hi"})

	o := exec(t, f.caseOf(in, nil))
	requireKind(t, o, pluginsdk.KindConnection, "certificate")

	requireKind(t, exec(t, f.caseOf(in, map[string]string{winrm.ConfigTLSCAPEM: "not pem"})), pluginsdk.KindValidation, winrm.ConfigTLSCAPEM)

	skip := input(map[string]any{"command": "echo hi", "insecure_skip_verify": true})
	requireKind(t, exec(t, f.caseOf(skip, nil)), pluginsdk.KindValidation, winrm.ConfigAllowInsecureWinRM)
	o = exec(t, f.caseOf(skip, map[string]string{winrm.ConfigAllowInsecureWinRM: "true"}))
	requireOK(t, o)
	warned := false
	for _, l := range o.Emitter.Lines() {
		warned = warned || strings.Contains(l.Line, "not verified")
	}
	if !warned {
		t.Fatal("no warning about the skipped verification")
	}
	// The gate needs exactly "true".
	requireKind(t, exec(t, f.caseOf(skip, map[string]string{winrm.ConfigAllowInsecureWinRM: "yes"})), pluginsdk.KindValidation, winrm.ConfigAllowInsecureWinRM)
}

func TestInputValidation(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"ok command":                 {`{"node":"n","command":"dir"}`, ""},
		"ok powershell":              {`{"node":"n","powershell":"Get-Date"}`, ""},
		"ok http ntlm":               {`{"node":"n","command":"dir","transport":"http","auth":"ntlm"}`, ""},
		"no node":                    {`{"command":"dir"}`, "node is required"},
		"neither":                    {`{"node":"n"}`, "exactly one"},
		"both":                       {`{"node":"n","command":"a","powershell":"b"}`, "exactly one"},
		"bad transport":              {`{"node":"n","command":"a","transport":"ftp"}`, "transport"},
		"bad auth":                   {`{"node":"n","command":"a","auth":"kerberos"}`, "auth"},
		"basic over http":            {`{"node":"n","command":"a","transport":"http","auth":"basic"}`, "clear text"},
		"http default basic is ntlm": {`{"node":"n","command":"a","transport":"http"}`, ""},
		"skip over http":             {`{"node":"n","command":"a","transport":"http","insecure_skip_verify":true}`, "no meaning over http"},
		"bad env name":               {`{"node":"n","command":"a","env":{"1X":"v"}}`, "variable name"},
		"nul in env":                 {"{\"node\":\"n\",\"command\":\"a\",\"env\":{\"X\":\"a\\u0000b\"}}", "NUL"},
		"nul in command":             {"{\"node\":\"n\",\"command\":\"a\\u0000b\"}", "NUL"},
		"negative code":              {`{"node":"n","command":"a","success_exit_codes":[-1]}`, "negative"},
		"unknown field":              {`{"node":"n","command":"a","shell":"cmd"}`, "invalid input"},
	} {
		t.Run(name, func(t *testing.T) {
			err := plug().Validate(context.Background(), json.RawMessage(tc.in))
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("Validate = %v; want %q", err, tc.want)
			}
		})
	}
}

// http needs both the step and the operator; basic over http is refused even then.
func TestHTTPNeedsTheOperatorOptIn(t *testing.T) {
	f := newFixture(t)
	in := input(map[string]any{"command": "echo hi", "transport": "http", "auth": "ntlm"})
	requireKind(t, exec(t, f.caseOf(in, nil)), pluginsdk.KindValidation, winrm.ConfigAllowInsecureWinRM)
	basic := input(map[string]any{"command": "echo hi", "transport": "http", "auth": "basic"})
	requireKind(t, exec(t, f.caseOf(basic, map[string]string{winrm.ConfigAllowInsecureWinRM: "true"})), pluginsdk.KindValidation, "clear text")
}

func TestNodeProblems(t *testing.T) {
	f := newFixture(t)
	in := input(map[string]any{"command": "echo hi"})
	c := f.caseOf(in, f.trusted())
	c.Nodes = nil
	requireKind(t, exec(t, c), pluginsdk.KindValidation, "not part of this step")

	n := f.node
	n.Transport = "ssh"
	c = f.caseOf(in, f.trusted())
	c.Nodes = map[string]pluginsdk.NodeInfo{"win1": n}
	requireKind(t, exec(t, c), pluginsdk.KindValidation, "needs a winrm node")
}

// Canceling terminates the command and deletes the shell within the agent's
// cancel grace, even though the fake never answers a Receive with data.
func TestCancelDeletesTheShell(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(500*time.Millisecond, cancel)
	start := time.Now()
	o := plugintest.Exec(ctx, t, plug(), f.caseOf(input(map[string]any{"powershell": "echo started\nblock"}), f.trusted()))
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("cancel took %s", d)
	}
	requireKind(t, o, pluginsdk.KindCancelled, "")
	deadline := time.Now().Add(5 * time.Second)
	for f.srv.ShellCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := f.srv.ShellCount(); n != 0 {
		t.Fatalf("%d shells left open on the node", n)
	}
}

func TestShellDeletedAfterSuccess(t *testing.T) {
	f := newFixture(t)
	requireOK(t, exec(t, f.caseOf(input(map[string]any{"command": "echo hi"}), f.trusted())))
	deadline := time.Now().Add(5 * time.Second)
	for f.srv.ShellCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := f.srv.ShellCount(); n != 0 {
		t.Fatalf("%d shells left open", n)
	}
}

// ntlm is the default; against a server that only offers Basic the negotiator
// never sends the credentials, so the step fails as an authentication error and
// the password does not appear anywhere.
func TestNTLMWithoutServerSupportIsAnAuthError(t *testing.T) {
	f := newFixture(t)
	o := exec(t, f.caseOf(input(map[string]any{"command": "echo hi", "auth": "ntlm"}), f.trusted()))
	requireKind(t, o, pluginsdk.KindAuth, "authentication")
	if strings.Contains(o.Result.Error.Message, password) {
		t.Fatal("the password is in the error")
	}
	if len(f.srv.Commands()) != 0 {
		t.Fatal("a command ran without authentication")
	}
}

// A step cancelled while the node is still creating the shell must not leave
// that shell behind once the node answers.
func TestCancelDuringShellCreationLeavesNoShell(t *testing.T) {
	srv := winrmtest.Start(t, winrmtest.Options{User: user, Password: password, CreateDelay: time.Second})
	f := &fixture{srv: srv, node: pluginsdk.NodeInfo{
		Name: "win1", Address: srv.Host(), Port: srv.Port(), Transport: "winrm", OS: "windows", Credential: credRef,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	o := plugintest.Exec(ctx, t, plug(), f.caseOf(input(map[string]any{"command": "echo hi"}), f.trusted()))
	if d := time.Since(start); d > 900*time.Millisecond {
		t.Fatalf("cancel took %s; it must not wait for the node", d)
	}
	requireKind(t, o, pluginsdk.KindCancelled, "")
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if f.srv.ShellCount() == 0 && len(f.srv.Commands()) == 0 {
			time.Sleep(1500 * time.Millisecond) // the node answers after 1 s; the orphan is then deleted
			if f.srv.ShellCount() == 0 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%d shells left open on the node", f.srv.ShellCount())
}

func TestEnvNamesDifferingInCaseAreRefused(t *testing.T) {
	err := plug().Validate(context.Background(), json.RawMessage(`{"node":"n","command":"a","env":{"Path":"1","PATH":"2"}}`))
	if err == nil || !strings.Contains(err.Error(), "differ only in case") {
		t.Fatalf("Validate = %v", err)
	}
	err = plug().Validate(context.Background(), json.RawMessage("{\"node\":\"n\",\"command\":\"a\\nb\"}"))
	if err == nil || !strings.Contains(err.Error(), "single line") {
		t.Fatalf("Validate = %v", err)
	}
}
