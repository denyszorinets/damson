package sshconn

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
)

func testKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestNewHostKeyCheckPolicies(t *testing.T) {
	pinned := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(testKey(t))))
	for name, tc := range map[string]struct {
		node   pluginsdk.NodeInfo
		config map[string]string
		want   string // substring of the error; empty means accepted
	}{
		"strict with key":     {pluginsdk.NodeInfo{HostKey: pinned}, nil, ""},
		"default is strict":   {pluginsdk.NodeInfo{}, nil, "no host_key"},
		"strict without key":  {pluginsdk.NodeInfo{HostKeyPolicy: PolicyStrict}, nil, "no host_key"},
		"tofu without key":    {pluginsdk.NodeInfo{HostKeyPolicy: PolicyTofu}, nil, ""},
		"insecure not opted":  {pluginsdk.NodeInfo{HostKeyPolicy: PolicyInsecure}, nil, "not enabled"},
		"insecure opted in":   {pluginsdk.NodeInfo{HostKeyPolicy: PolicyInsecure}, map[string]string{ConfigAllowInsecureHostKeys: "true"}, ""},
		"unknown policy":      {pluginsdk.NodeInfo{HostKeyPolicy: "whatever"}, nil, "unknown host key policy"},
		"unparsable host_key": {pluginsdk.NodeInfo{HostKey: "nope"}, nil, "invalid host_key"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newHostKeyCheck(pluginsdk.ExecRequest{Config: tc.config}, tc.node)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("err = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestHostKeyCallbackPinnedMismatch(t *testing.T) {
	pinned, other := testKey(t), testKey(t)
	h := &HostKeyCheck{Policy: PolicyTofu, Pinned: pinned}
	if err := h.callback("", nil, pinned); err != nil {
		t.Fatal(err)
	}
	if err := h.callback("", nil, other); err == nil || h.mismatch == nil {
		t.Fatalf("a different key was accepted: %v", err)
	}
}

type reporter struct{ node, key string }

func (r *reporter) ReportHostKey(node, key string) { r.node, r.key = node, key }

func TestReport(t *testing.T) {
	k := testKey(t)
	node := pluginsdk.NodeInfo{Name: "web-1"}
	for name, tc := range map[string]struct {
		h        HostKeyCheck
		reported bool
	}{
		"tofu first use":  {HostKeyCheck{Policy: PolicyTofu, Observed: k}, true},
		"tofu but pinned": {HostKeyCheck{Policy: PolicyTofu, Pinned: k, Observed: k}, false},
		"strict":          {HostKeyCheck{Policy: PolicyStrict, Pinned: k, Observed: k}, false},
		"insecure":        {HostKeyCheck{Policy: PolicyInsecure, Observed: k}, false},
	} {
		t.Run(name, func(t *testing.T) {
			r := &reporter{}
			tc.h.Report(pluginsdk.ExecRequest{HostKeys: r}, node, &plugintest.FakeEmitter{})
			if (r.node != "") != tc.reported || tc.reported && r.node != "web-1" {
				t.Fatalf("reported %q %q; want reported=%v", r.node, r.key, tc.reported)
			}
		})
	}
	// A nil reporter must be tolerated.
	h := HostKeyCheck{Policy: PolicyTofu, Observed: k}
	h.Report(pluginsdk.ExecRequest{}, node, &plugintest.FakeEmitter{})
}
