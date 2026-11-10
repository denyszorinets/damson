package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

const (
	testPassword = "S3cr3t-Pa55!/x"
	testKeyLine1 = "AAAABBBBCCCCDDDD"
	testKeyLine2 = "EEEEFFFFGGGGHHHH"
	testKey      = "-----BEGIN KEY-----\n" + testKeyLine1 + "\n" + testKeyLine2 + "\n-----END KEY-----"
)

func testBundle() protocol.SecretBundle {
	return protocol.SecretBundle{
		"secret://t/pw":  {"value": testPassword, "username": "admin42"},
		"secret://t/key": {"private_key": testKey},
	}
}

// leaks lists every form of the secrets that must never reach the controller
// outside step.execute.
func leaks() []string {
	var out []string
	for _, s := range []string{testPassword, "admin42", testKeyLine1, testKeyLine2, testKey} {
		out = append(out, secretVariants(s)...)
	}
	return out
}

func TestMaskerVariants(t *testing.T) {
	m := newMasker(testBundle())
	enc := base64.StdEncoding.EncodeToString([]byte(testPassword))
	tests := []struct {
		name, in, want string
	}{
		{"plain", "pw=" + testPassword + " done", "pw=*** done"},
		{"base64", "auth " + enc, "auth ***"},
		{"base64 url", base64.URLEncoding.EncodeToString([]byte(testPassword)), "***"},
		{"raw base64", base64.RawStdEncoding.EncodeToString([]byte(testPassword)), "***"},
		{"query escaped", "?p=" + url.QueryEscape(testPassword), "?p=***"},
		{"path escaped", "/" + url.PathEscape(testPassword), "/***"},
		{"short field", "user admin42 logged in", "user *** logged in"},
		{"one line of a multi-line secret", "got " + testKeyLine2, "got ***"},
		{"whole multi-line secret", testKey, "***"},
		{"two secrets", testPassword + "|" + testKeyLine1, "***|***"},
		{"nothing to mask", "hello world", "hello world"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.Mask(tt.in); got != tt.want {
				t.Errorf("Mask(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
	if got := newMasker(nil).Mask("x"); got != "x" {
		t.Errorf("empty bundle masks %q", got)
	}
	var nilMasker *masker
	if got := nilMasker.Mask("x"); got != "x" {
		t.Errorf("nil masker = %q", got)
	}
}

func TestMaskerShortLinesOfMultiLineSecretStayVisible(t *testing.T) {
	m := newMasker(protocol.SecretBundle{"r": {"v": "abcdefgh\nxyz\n1234"}})
	// Lines of four characters or fewer are not masked on their own, the
	// whole value still is.
	if got := m.Mask("xyz and 1234 and abcdefgh"); got != "xyz and 1234 and ***" {
		t.Errorf("got %q", got)
	}
}

func TestSanitizeOutputs(t *testing.T) {
	m := newMasker(testBundle())
	raw := json.RawMessage(`{"a":"fine","leak":"x ` + testPassword + `","n":3,"f":1.5,"list":["ok","` + base64.StdEncoding.EncodeToString([]byte(testPassword)) + `"],"deep":{"k":"` + testKeyLine1 + `"}}`)
	out, redacted, err := sanitizeOutputs(raw, m)
	if err != nil {
		t.Fatal(err)
	}
	if out["a"] != "fine" || out["leak"] != "***" || out["n"] != int64(3) || out["f"] != 1.5 {
		t.Errorf("out = %v", out)
	}
	if l := out["list"].([]any); l[0] != "ok" || l[1] != "***" {
		t.Errorf("list = %v", l)
	}
	if out["deep"].(map[string]any)["k"] != "***" {
		t.Errorf("deep = %v", out["deep"])
	}
	if len(redacted) != 3 {
		t.Errorf("redacted paths = %v", redacted)
	}

	for _, bad := range []string{`[1]`, `"s"`, `{`, `{} {}`} {
		if _, _, err := sanitizeOutputs(json.RawMessage(bad), m); err == nil {
			t.Errorf("sanitizeOutputs(%s) accepted", bad)
		}
	}
	for _, none := range []string{``, `null`, `  `} {
		if out, _, err := sanitizeOutputs(json.RawMessage(none), m); err != nil || out != nil {
			t.Errorf("sanitizeOutputs(%q) = %v, %v", none, out, err)
		}
	}
}

func TestBundleResolver(t *testing.T) {
	r := newBundleResolver(testBundle())
	if v, err := r.Resolve("secret://t/pw"); err != nil || v != testPassword {
		t.Errorf("value = %q, %v", v, err)
	}
	if v, err := r.Resolve("secret://t/pw#username"); err != nil || v != "admin42" {
		t.Errorf("field = %q, %v", v, err)
	}
	f, err := r.ResolveFields("secret://t/key")
	if err != nil || f["private_key"] != testKey {
		t.Errorf("fields = %v, %v", f, err)
	}
	f["private_key"] = "changed" // a copy: the bundle is unaffected
	if f2, _ := r.ResolveFields("secret://t/key"); f2["private_key"] != testKey {
		t.Error("ResolveFields returned the bundle's own map")
	}
	for _, ref := range []string{"secret://t/none", "secret://t/pw#nope"} {
		_, err := r.Resolve(ref)
		if err == nil || strings.Contains(err.Error(), testPassword) {
			t.Errorf("Resolve(%q) error = %v", ref, err)
		}
	}
	r.wipe()
	if _, err := r.Resolve("secret://t/pw"); err == nil {
		t.Error("resolved after wipe")
	}
}

// TestSecretsNeverLeave is the masking test for the secret bundle path
// (hard rule 5): the plugin gets the values, but they appear in no frame the
// agent sends, in no agent log line, and in no log, output or error.
func TestSecretsNeverLeave(t *testing.T) {
	ctl := newFakeCtl(t)
	ctl.autoAck.Store(true)
	enc := base64.StdEncoding.EncodeToString([]byte(testPassword))
	p := testPlugin{name: "demo", exec: func(_ context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
		pw, err := req.Secrets.Resolve("secret://t/pw")
		if err != nil || pw != testPassword {
			return pluginsdk.Result{}, errors.New("plugin could not resolve the secret")
		}
		emit.Log(pluginsdk.Stdout, "password is "+pw)
		emit.Log(pluginsdk.Stderr, "Authorization: Basic "+enc)
		emit.Log(pluginsdk.Stdout, "https://h/?p="+url.QueryEscape(pw))
		emit.Log(pluginsdk.Stdout, "key line "+testKeyLine2)
		emit.Progress(10, "using "+pw)
		req.Logger.Warn("diagnostic "+pw, "pw", pw, "err", errors.New("failed with "+pw))
		out, _ := json.Marshal(map[string]any{"token": "t-" + pw, "ok": "fine"})
		return pluginsdk.Result{Outputs: out}, nil
	}}
	_, agentLog := startAgent(t, ctl, []pluginsdk.Plugin{p})

	run := func(attempt string) protocol.StepComplete {
		req := stepReq(attempt)
		req.Secrets = testBundle()
		ctl.mustExecute(req)
		return completeOf(t, ctl, attempt)
	}
	ok := run("ok")
	if ok.Status != protocol.StatusSucceeded || ok.Outputs["token"] != "***" || ok.Outputs["ok"] != "fine" {
		t.Errorf("success complete = %+v", ok)
	}
	lines := ctl.lines("ok")
	wantMasked := []string{"password is ***", "Authorization: Basic ***", "https://h/?p=***", "key line ***"}
	for i, want := range wantMasked {
		if got := lines[uint64(i+1)].Line; got != want {
			t.Errorf("log %d = %q, want %q", i+1, got, want)
		}
	}
	assertNoLeak(t, ctl, agentLog)
}

func TestSecretsNeverLeaveOnFailureAndPanic(t *testing.T) {
	for _, mode := range []string{"error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			ctl := newFakeCtl(t)
			p := plug(func(_ context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
				pw, _ := req.Secrets.Resolve("secret://t/pw")
				req.Logger.Error("diagnostic", "pw", pw, "err", errors.New("with "+pw))
				if mode == "panic" {
					panic("panic carrying " + pw)
				}
				return pluginsdk.Result{}, errors.New("login with " + pw + " failed")
			})
			_, agentLog := startAgent(t, ctl, []pluginsdk.Plugin{p})
			req := stepReq("a1")
			req.Secrets = testBundle()
			ctl.mustExecute(req)
			c := completeOf(t, ctl, "a1")
			if c.Status != protocol.StatusFailed || c.Error == nil || strings.Contains(c.Error.Message, testPassword) {
				t.Errorf("complete = %+v", c)
			}
			if mode == "error" && c.Error.Message != "login with *** failed" {
				t.Errorf("error message = %q", c.Error.Message)
			}
			assertNoLeak(t, ctl, agentLog)
		})
	}
}

// assertNoLeak scans every frame the agent sent and the agent's own log.
func assertNoLeak(t *testing.T, ctl *fakeCtl, agentLog *syncBuf) {
	t.Helper()
	eventually(t, "frames recorded", func() bool { return len(ctl.rawFrames()) > 3 })
	logText := agentLog.String()
	for _, needle := range leaks() {
		if needle == "" {
			continue
		}
		for _, f := range ctl.rawFrames() {
			if bytes.Contains(f, []byte(needle)) {
				t.Fatalf("a frame sent to the controller contains a secret form (%d bytes)", len(needle))
			}
		}
		if strings.Contains(logText, needle) {
			t.Fatalf("the agent log contains a secret form (%d bytes)", len(needle))
		}
	}
}
