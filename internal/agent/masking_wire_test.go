package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// These tests pin ADR-0047: the agent is the only masking boundary, so no
// frame it sends may carry a secret in any common encoding. The forms are
// computed here, not by the masker, so a bug in the masker cannot hide itself.

const (
	wirePassword   = "Wire-Pa55word!/q"
	wirePassphrase = "Pass-phr4se#9"
	wireKeyLine1   = "WWWWXXXXYYYY1111"
	wireKeyLine2   = "ZZZZ2222AAAA3333"
	wireKey        = "-----BEGIN OPENSSH PRIVATE KEY-----\n" + wireKeyLine1 + "\n" + wireKeyLine2 + "\n-----END OPENSSH PRIVATE KEY-----"
)

// wireBundle holds a step secret and a node-credential-style ssh_key.
func wireBundle() protocol.SecretBundle {
	return protocol.SecretBundle{
		"secret://p/token":       {"value": wirePassword},
		"secret://_inventory/ci": {"private_key": wireKey, "passphrase": wirePassphrase},
	}
}

// wireForms lists the plain and encoded forms of each value, and of each line
// of a multi-line value that is long enough to be masked on its own.
func wireForms() []string {
	values := []string{wirePassword, wirePassphrase, wireKey, wireKeyLine1, wireKeyLine2}
	var out []string
	for _, v := range values {
		out = append(out, v,
			base64.StdEncoding.EncodeToString([]byte(v)), base64.RawStdEncoding.EncodeToString([]byte(v)),
			base64.URLEncoding.EncodeToString([]byte(v)), base64.RawURLEncoding.EncodeToString([]byte(v)),
			url.QueryEscape(v), url.PathEscape(v))
	}
	return out
}

func assertNoWireLeak(t *testing.T, ctl *fakeCtl, agentLog *syncBuf) {
	t.Helper()
	eventually(t, "frames recorded", func() bool { return len(ctl.rawFrames()) > 3 })
	text := agentLog.String()
	for _, form := range wireForms() {
		for _, f := range ctl.rawFrames() {
			if bytes.Contains(f, []byte(form)) {
				t.Fatalf("a frame sent to the controller contains a secret form (%d bytes)", len(form))
			}
		}
		if strings.Contains(text, form) {
			t.Fatalf("the agent log contains a secret form (%d bytes)", len(form))
		}
	}
}

// emitEveryForm sends each form through every channel the plugin has.
func emitEveryForm(req pluginsdk.ExecRequest, emit pluginsdk.Emitter) {
	for i, form := range wireForms() {
		emit.Log(pluginsdk.Stdout, fmt.Sprintf("out %d %s", i, form))
		emit.Log(pluginsdk.Stderr, fmt.Sprintf("err %d %s", i, form))
	}
	emit.Progress(50, "progress "+wirePassword+" "+base64.StdEncoding.EncodeToString([]byte(wirePassphrase)))
	req.Logger.Warn("diagnostic "+wireKeyLine1, "pass", wirePassphrase, "err", errors.New("failed with "+wirePassword))
	// A secret straddling the split of a very long line.
	pad := strings.Repeat("p", maxLineBytes-len(wirePassword)/2)
	emit.Log(pluginsdk.Stdout, pad+wirePassword+" tail")
}

func TestSecretsNeverReachTheWire(t *testing.T) {
	ctl := newFakeCtl(t)
	ctl.autoAck.Store(true)
	p := plug(func(_ context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
		emitEveryForm(req, emit)
		out := map[string]any{wirePassword: "k-" + wirePassphrase, "ok": "fine", "nested": map[string]any{"key": wireKey}}
		raw, _ := json.Marshal(out)
		return pluginsdk.Result{Outputs: raw}, errors.New("failed with " + wireKeyLine2 + " and " + wirePassword)
	})
	_, agentLog := startAgent(t, ctl, []pluginsdk.Plugin{p})
	req := stepReq("w1")
	req.Secrets = wireBundle()
	ctl.mustExecute(req)
	c := completeOf(t, ctl, "w1")

	// The plugin ran and was masked, not skipped.
	masked := 0
	for _, e := range ctl.lines("w1") {
		if strings.Contains(e.Line, "***") {
			masked++
		}
	}
	if masked < len(wireForms()) {
		t.Errorf("only %d masked log lines", masked)
	}
	if c.Error == nil || !strings.Contains(c.Error.Message, "***") {
		t.Errorf("complete error = %+v", c.Error)
	}
	assertNoWireLeak(t, ctl, agentLog)
}

// Resent batches (after a reconnect, from the spool, with a declared gap) carry
// no secret either: the spool only ever holds masked lines.
func TestResentLogsHoldNoSecret(t *testing.T) {
	ctl := newFakeCtl(t)
	release := make(chan struct{})
	logged := make(chan struct{})
	p := plug(func(_ context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
		for i, form := range wireForms() {
			emit.Log(pluginsdk.Stdout, fmt.Sprintf("n%02d %s %s", i, form, strings.Repeat("z", 100)))
			time.Sleep(10 * time.Millisecond) // one batch per line
		}
		close(logged)
		<-release
		return pluginsdk.Result{}, nil
	})
	_, agentLog := startAgent(t, ctl, []pluginsdk.Plugin{p}, func(c *Config) {
		c.MemBufferBytes, c.SpoolBytes = 1, 4000 // small enough that the spool overflows
	})
	req := stepReq("w2")
	req.Secrets = wireBundle()
	ctl.mustExecute(req)
	<-logged

	before := len(ctl.logsOf("w2"))
	_ = ctl.latest().peer.Close()
	ctl.waitHellos(2)
	eventually(t, "resend after the reconnect", func() bool {
		bs := ctl.logsOf("w2")
		return len(bs) > before
	})
	ctl.autoAck.Store(true)
	close(release)
	completeOf(t, ctl, "w2")
	assertNoWireLeak(t, ctl, agentLog)
}
