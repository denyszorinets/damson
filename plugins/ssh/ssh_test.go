//go:build unix

package ssh_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
	"github.com/denyszorinets/damson/plugins/internal/sshtest"
	sshplugin "github.com/denyszorinets/damson/plugins/ssh"
)

func plug() pluginsdk.Plugin { return sshplugin.Plugins()[0] }

func exec(t *testing.T, c plugintest.Case) plugintest.Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return plugintest.Exec(ctx, t, plug(), c)
}

func decodeOut(t *testing.T, o plugintest.Outcome) sshplugin.Output {
	t.Helper()
	var out sshplugin.Output
	if err := json.Unmarshal(o.Result.Outputs, &out); err != nil {
		t.Fatalf("outputs %s: %v", o.Result.Outputs, err)
	}
	return out
}

func requireOK(t *testing.T, o plugintest.Outcome) {
	t.Helper()
	if o.Err != nil || o.Result.Error != nil {
		t.Fatalf("unexpected failure: err=%v step=%v", o.Err, o.Result.Error)
	}
}

func requireKind(t *testing.T, o plugintest.Outcome, kind pluginsdk.ErrorKind) *pluginsdk.StepError {
	t.Helper()
	if o.Err != nil {
		t.Fatalf("unexpected plugin fault: %v", o.Err)
	}
	if o.Result.Error == nil || o.Result.Error.Kind != kind {
		t.Fatalf("step error = %v, want kind %s", o.Result.Error, kind)
	}
	return o.Result.Error
}

func logLines(o plugintest.Outcome, s pluginsdk.Stream) []string {
	var out []string
	for _, l := range o.Emitter.Lines() {
		if l.Stream == s {
			out = append(out, l.Line)
		}
	}
	return out
}

func TestContract(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	blocking := f.Case("blocking", `{"command":"sleep 30"}`)
	plugintest.RunContract(t, plug(), plugintest.Options{
		Cases: []plugintest.Case{
			f.Case("command", `{"command":"echo hello"}`),
			f.Case("script", `{"script":"echo from script\n","env":{"A":"b"}}`),
		},
		Blocking: &blocking,
	})
}

func TestSource(t *testing.T) { plugintest.CheckSource(t, ".") }

func TestExecuteSuccess(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	o := exec(t, f.Case("ok", `{"command":"echo out; echo err >&2; echo '##damson-output k=v'; echo '##damson-output-json {\"n\":1,\"s\":\"x\"}'; echo last"}`))
	requireOK(t, o)
	out := decodeOut(t, o)
	if out.ExitCode != 0 || out.StdoutTail != "out\nlast\n" || out.StderrTail != "err\n" {
		t.Errorf("output = %+v", out)
	}
	if out.Values["k"] != "v" || out.Values["n"] != float64(1) || out.Values["s"] != "x" {
		t.Errorf("values = %v", out.Values)
	}
	if got := logLines(o, pluginsdk.Stdout); strings.Join(got, ",") != "out,last" {
		t.Errorf("stdout log = %v (protocol lines must be stripped)", got)
	}
	if got := logLines(o, pluginsdk.Stderr); strings.Join(got, ",") != "err" {
		t.Errorf("stderr log = %v", got)
	}
}

func TestExitCodes(t *testing.T) {
	tests := []struct {
		name, input string
		wantKind    pluginsdk.ErrorKind // empty: success
		wantCode    int
	}{
		{"non-zero", `{"command":"echo bad >&2; exit 3"}`, pluginsdk.KindExitCode, 3},
		{"success code", `{"command":"exit 3","success_exit_codes":[0,3]}`, "", 3},
		{"zero not listed", `{"command":"exit 0","success_exit_codes":[3]}`, pluginsdk.KindExitCode, 0},
		{"script", `{"script":"#!/bin/sh\nexit 4\n"}`, pluginsdk.KindExitCode, 4},
		{"signal", `{"command":"kill -9 $$"}`, pluginsdk.KindExitCode, -1},
	}
	f := sshtest.NewFixture(t, "")
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := exec(t, f.Case(tc.name, tc.input))
			if tc.wantKind == "" {
				requireOK(t, o)
			} else {
				se := requireKind(t, o, tc.wantKind)
				if se.ExitCode != nil && *se.ExitCode != tc.wantCode {
					t.Errorf("error exit code = %d, want %d", *se.ExitCode, tc.wantCode)
				}
			}
			if got := decodeOut(t, o).ExitCode; got != tc.wantCode {
				t.Errorf("exit_code output = %d, want %d", got, tc.wantCode)
			}
		})
	}
}

func TestEnvWorkdirScript(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	in, _ := json.Marshal(map[string]any{
		"command": `echo "$FOO"; pwd`, "workdir": dir,
		"env": map[string]string{"FOO": `a b"c'd$e`},
	})
	o := exec(t, f.Case("env", string(in)))
	requireOK(t, o)
	if got := decodeOut(t, o).StdoutTail; got != "a b\"c'd$e\n"+dir+"\n" {
		t.Errorf("stdout = %q", got)
	}

	in, _ = json.Marshal(map[string]any{
		"script": "echo \"$X\"\ncat <<'EOF'\nnested heredoc\nEOF\n", "env": map[string]string{"X": "val"},
		"interpreter": "/bin/sh -e",
	})
	o = exec(t, f.Case("script", string(in)))
	requireOK(t, o)
	if got := decodeOut(t, o).StdoutTail; got != "val\nnested heredoc\n" {
		t.Errorf("script stdout = %q", got)
	}
}

func TestPtyRequested(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	o := exec(t, f.Case("pty", `{"command":"echo hi","pty":true}`))
	requireOK(t, o)
	if f.Server.PtyRequests() != 1 || decodeOut(t, o).StdoutTail != "hi\n" {
		t.Errorf("ptys=%d out=%+v", f.Server.PtyRequests(), decodeOut(t, o))
	}
}

func TestCancelAndTimeout(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	c := f.Case("sleep", `{"command":"echo started; sleep 30"}`)

	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		time.AfterFunc(500*time.Millisecond, cancel)
		start := time.Now()
		o := plugintest.Exec(ctx, t, plug(), c)
		_ = requireKind(t, o, pluginsdk.KindCancelled)
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("cancel took %s", d)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		start := time.Now()
		o := plugintest.Exec(ctx, t, plug(), c)
		_ = requireKind(t, o, pluginsdk.KindTimeout)
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("timeout took %s", d)
		}
	})
}

func TestHostKeyPolicies(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	other := sshtest.KeyLine(sshtest.NewSigner(t).PublicKey())
	tests := []struct {
		name     string
		mutate   func(n *pluginsdk.NodeInfo)
		config   map[string]string
		wantKind pluginsdk.ErrorKind // empty: success
		wantMsg  string
		wantKeys int // keys reported through the SDK hook
	}{
		{"strict pinned", func(*pluginsdk.NodeInfo) {}, nil, "", "", 0},
		{"strict default policy", func(n *pluginsdk.NodeInfo) { n.HostKeyPolicy = "" }, nil, "", "", 0},
		{"strict mismatch", func(n *pluginsdk.NodeInfo) { n.HostKey = other }, nil, pluginsdk.KindConnection, "host key mismatch", 0},
		{"strict no key", func(n *pluginsdk.NodeInfo) { n.HostKey = "" }, nil, pluginsdk.KindValidation, "no host_key", 0},
		{"strict garbage key", func(n *pluginsdk.NodeInfo) { n.HostKey = "nonsense" }, nil, pluginsdk.KindValidation, "invalid host_key", 0},
		{"tofu reports", func(n *pluginsdk.NodeInfo) { n.HostKey = ""; n.HostKeyPolicy = "tofu" }, nil, "", "", 1},
		{"tofu pinned matches", func(n *pluginsdk.NodeInfo) { n.HostKeyPolicy = "tofu" }, nil, "", "", 0},
		{"tofu pinned mismatch", func(n *pluginsdk.NodeInfo) { n.HostKey = other; n.HostKeyPolicy = "tofu" }, nil, pluginsdk.KindConnection, "host key mismatch", 0},
		{"insecure not enabled", func(n *pluginsdk.NodeInfo) { n.HostKey = ""; n.HostKeyPolicy = "insecure" }, nil, pluginsdk.KindValidation, "not enabled", 0},
		{"insecure enabled", func(n *pluginsdk.NodeInfo) { n.HostKey = ""; n.HostKeyPolicy = "insecure" },
			map[string]string{sshplugin.ConfigAllowInsecureHostKeys: "true"}, "", "", 0},
		{"unknown policy", func(n *pluginsdk.NodeInfo) { n.HostKeyPolicy = "yolo" }, nil, pluginsdk.KindValidation, "unknown host key policy", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := f.Case(tc.name, `{"command":"true"}`)
			n := f.Node
			tc.mutate(&n)
			c.Nodes = map[string]pluginsdk.NodeInfo{sshtest.NodeName: n}
			c.Config = tc.config
			o := exec(t, c)
			if tc.wantKind == "" {
				requireOK(t, o)
			} else if se := requireKind(t, o, tc.wantKind); !strings.Contains(se.Message, tc.wantMsg) {
				t.Errorf("message %q does not contain %q", se.Message, tc.wantMsg)
			}
			keys := o.HostKeys.Keys()
			if len(keys) != tc.wantKeys {
				t.Fatalf("reported keys = %v, want %d", keys, tc.wantKeys)
			}
			if tc.wantKeys == 1 && (keys[0].Node != sshtest.NodeName || keys[0].Key != f.Server.HostKeyLine()) {
				t.Errorf("reported %v, want the server host key", keys[0])
			}
		})
	}
}

func TestTofuWithoutReporter(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	n := f.Node
	n.HostKey, n.HostKeyPolicy = "", "tofu"
	req := pluginsdk.ExecRequest{
		Input:   json.RawMessage(`{"node":"target","command":"true"}`),
		Secrets: plugintest.FakeBundle(f.Bundle),
		Nodes:   map[string]pluginsdk.NodeInfo{"target": n},
	}
	res, err := plug().Execute(context.Background(), req, &plugintest.FakeEmitter{})
	if err != nil || res.Error != nil {
		t.Fatalf("Execute with nil HostKeys failed: %v %v", err, res.Error)
	}
}

func TestAuth(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	otherKey, _ := sshtest.NewClientKey(t, "")
	enc := sshtest.NewFixture(t, "hunter2-passphrase")
	tests := []struct {
		name     string
		f        *sshtest.Fixture
		fields   map[string]string
		wantKind pluginsdk.ErrorKind
		wantMsg  string
	}{
		{"valid key", f, nil, "", ""},
		{"encrypted key", enc, nil, "", ""},
		{"wrong key", f, map[string]string{"username": "damson", "private_key": otherKey}, pluginsdk.KindAuth, "authentication"},
		{"wrong user", f, map[string]string{"username": "mallory", "private_key": f.PrivateKey}, pluginsdk.KindAuth, "authentication"},
		{"missing passphrase", enc, map[string]string{"username": "damson", "private_key": enc.PrivateKey}, pluginsdk.KindAuth, "no passphrase"},
		{"wrong passphrase", enc, map[string]string{"username": "damson", "private_key": enc.PrivateKey, "passphrase": "nope"}, pluginsdk.KindAuth, "could not be parsed"},
		{"garbage key", f, map[string]string{"username": "damson", "private_key": "not a key"}, pluginsdk.KindAuth, "could not be parsed"},
		{"no username", f, map[string]string{"private_key": f.PrivateKey}, pluginsdk.KindAuth, "no username"},
		{"no secret", f, map[string]string{"username": "damson"}, pluginsdk.KindAuth, "neither"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.f.Case(tc.name, `{"command":"true"}`)
			if tc.fields != nil {
				c.Bundle = map[string]map[string]string{sshtest.CredentialRef: tc.fields}
			}
			o := exec(t, c)
			if tc.wantKind == "" {
				requireOK(t, o)
				return
			}
			if se := requireKind(t, o, tc.wantKind); !strings.Contains(se.Message, tc.wantMsg) {
				t.Errorf("message %q does not contain %q", se.Message, tc.wantMsg)
			}
		})
	}
}

func TestCredentialOverrideAndNodeErrors(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	c := f.Case("override", `{"command":"true","credential":"secret://other/key"}`)
	c.Bundle = map[string]map[string]string{"secret://other/key": f.Bundle[sshtest.CredentialRef]}
	n := f.Node
	n.Credential = "secret://missing"
	c.Nodes = map[string]pluginsdk.NodeInfo{sshtest.NodeName: n}
	requireOK(t, exec(t, c))

	c = f.Case("unknown node", `{"command":"true","node":"nope"}`)
	_ = requireKind(t, exec(t, c), pluginsdk.KindValidation)

	c = f.Case("password", `{"command":"true"}`)
	c.Bundle = map[string]map[string]string{sshtest.CredentialRef: {"username": "damson", "password": "pw"}}
	_ = requireKind(t, exec(t, c), pluginsdk.KindAuth) // fixture server has no password auth
}

func TestUnreachable(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	c := f.Case("down", `{"command":"true"}`)
	n := f.Node
	f.Server.Close()
	c.Nodes = map[string]pluginsdk.NodeInfo{sshtest.NodeName: n}
	_ = requireKind(t, exec(t, c), pluginsdk.KindConnection)
}

// TestSecretsNeverLeak runs success and failure paths with a credential
// bundle and a secret env value, and requires that no secret value shows up in
// logs, logger output, outputs or errors (ARCHITECTURE.md section 9.4).
func TestSecretsNeverLeak(t *testing.T) {
	const pass = "hunter2-passphrase"
	f := sshtest.NewFixture(t, pass)
	keyBody := strings.Split(f.PrivateKey, "\n")[1]
	secrets := map[string]string{
		"key": f.PrivateKey, "key body": keyBody, "passphrase": pass,
		"token": "tok-3a9f-very-secret", "password": "pw-very-secret",
	}
	bundle := map[string]map[string]string{
		sshtest.CredentialRef: f.Bundle[sshtest.CredentialRef],
		"secret://app/token":  {"value": secrets["token"]},
	}
	withPassword := map[string]string{"username": "damson", "private_key": f.PrivateKey, "passphrase": pass, "password": secrets["password"]}

	tests := []struct {
		name   string
		input  string
		mutate func(*plugintest.Case)
		kind   pluginsdk.ErrorKind
	}{
		{"success", `{"command":"test -n \"$TOKEN\" && echo token-set","env":{"TOKEN":"secret://app/token"}}`, nil, ""},
		{"exit error", `{"command":"exit 2","env":{"TOKEN":"secret://app/token"}}`, nil, pluginsdk.KindExitCode},
		{"script", `{"script":"test -n \"$TOKEN\"","env":{"TOKEN":"secret://app/token"}}`, nil, ""},
		{"password and key", `{"command":"true"}`, func(c *plugintest.Case) {
			c.Bundle[sshtest.CredentialRef] = withPassword
		}, ""},
		{"wrong passphrase", `{"command":"true"}`, func(c *plugintest.Case) {
			c.Bundle[sshtest.CredentialRef] = map[string]string{"username": "damson", "private_key": f.PrivateKey, "passphrase": secrets["password"]}
		}, pluginsdk.KindAuth},
		{"host key mismatch", `{"command":"true"}`, func(c *plugintest.Case) {
			n := f.Node
			n.HostKey = sshtest.KeyLine(sshtest.NewSigner(t).PublicKey())
			c.Nodes = map[string]pluginsdk.NodeInfo{sshtest.NodeName: n}
		}, pluginsdk.KindConnection},
		{"unresolvable env secret", `{"command":"true","env":{"TOKEN":"secret://app/missing"}}`, nil, pluginsdk.KindAuth},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := f.Case(tc.name, tc.input)
			c.Bundle = map[string]map[string]string{}
			for k, v := range bundle {
				c.Bundle[k] = v
			}
			if tc.mutate != nil {
				tc.mutate(&c)
			}
			o := exec(t, c)
			if tc.kind == "" {
				requireOK(t, o)
			} else {
				_ = requireKind(t, o, tc.kind)
			}
			if leaked := plugintest.LeakedSecrets(secrets, o); len(leaked) > 0 {
				t.Errorf("secrets leaked: %v", leaked)
			}
		})
	}
}
