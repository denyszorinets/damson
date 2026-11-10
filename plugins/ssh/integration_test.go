//go:build integration && unix

package ssh_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	gossh "golang.org/x/crypto/ssh"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
	"github.com/denyszorinets/damson/plugins/internal/sshtest"
)

const (
	itUser = "damson"
	itPort = "2222/tcp"
)

// openssh is a running OpenSSH container with one authorized key.
type openssh struct {
	node       pluginsdk.NodeInfo
	privateKey string
}

// startOpenSSH starts linuxserver/openssh-server, which runs a real sshd.
func startOpenSSH(t *testing.T) *openssh {
	t.Helper()
	priv, pub := sshtest.NewClientKey(t, "")
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			// Pinned. Assumptions about this image: sshd listens on 2222, the user
			// named by USER_NAME is created with the key in PUBLIC_KEY, password
			// login is off, and the user's login shell can run /bin/sh -s.
			Image:        "lscr.io/linuxserver/openssh-server:version-10.3_p1-r1",
			ExposedPorts: []string{itPort},
			Env: map[string]string{
				"PUID": "1000", "PGID": "1000", "TZ": "UTC",
				"USER_NAME": itUser, "PUBLIC_KEY": sshtest.KeyLine(pub),
				"PASSWORD_ACCESS": "false", "SUDO_ACCESS": "false",
			},
			WaitingFor: wait.ForListeningPort(itPort).WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("cannot start sshd container (is Docker available?): %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := ctr.MappedPort(ctx, itPort)
	if err != nil {
		t.Fatal(err)
	}
	return &openssh{
		privateKey: priv,
		node: pluginsdk.NodeInfo{
			Name: sshtest.NodeName, Address: host, Port: int(port.Num()), Transport: "ssh", OS: "linux",
			Credential: sshtest.CredentialRef, HostKeyPolicy: "tofu",
		},
	}
}

func (o *openssh) caseFor(name, input string, node pluginsdk.NodeInfo) plugintest.Case {
	var m map[string]any
	if err := json.Unmarshal([]byte(input), &m); err != nil {
		panic(err)
	}
	m["node"] = sshtest.NodeName
	raw, _ := json.Marshal(m)
	return plugintest.Case{
		Name: name, Input: raw,
		Nodes:  map[string]pluginsdk.NodeInfo{sshtest.NodeName: node},
		Bundle: map[string]map[string]string{sshtest.CredentialRef: {"username": itUser, "private_key": o.privateKey}},
	}
}

// learnHostKey connects with the tofu policy until sshd is ready and returns
// the reported host key.
func (o *openssh) learnHostKey(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		out := exec(t, o.caseFor("tofu", `{"command":"true"}`, o.node))
		if out.Result.Error == nil && out.Err == nil {
			keys := out.HostKeys.Keys()
			if len(keys) != 1 || keys[0].Node != sshtest.NodeName || !validAuthorizedKey(keys[0].Key) {
				t.Fatalf("tofu reported %v, want one ssh host key for %s", keys, sshtest.NodeName)
			}
			return keys[0].Key
		}
		if time.Now().After(deadline) {
			t.Fatalf("sshd never accepted the key: %v %v", out.Err, out.Result.Error)
		}
		time.Sleep(time.Second)
	}
}

// validAuthorizedKey reports whether line is one public key in authorized_keys
// format. The container may use any host key type (ed25519, ecdsa, rsa).
func validAuthorizedKey(line string) bool {
	_, _, _, rest, err := gossh.ParseAuthorizedKey([]byte(line))
	return err == nil && len(rest) == 0
}

func TestIntegrationOpenSSH(t *testing.T) {
	o := startOpenSSH(t)
	hostKey := o.learnHostKey(t)
	strict := o.node
	strict.HostKeyPolicy, strict.HostKey = "strict", hostKey

	t.Run("success", func(t *testing.T) {
		res := exec(t, o.caseFor("ok", `{"command":"echo out; echo err >&2; echo '##damson-output who='$(id -un)"}`, strict))
		requireOK(t, res)
		out := decodeOut(t, res)
		if out.StdoutTail != "out\n" || out.StderrTail != "err\n" || out.Values["who"] != itUser {
			t.Errorf("output = %+v", out)
		}
	})

	t.Run("script with env", func(t *testing.T) {
		res := exec(t, o.caseFor("script", `{"script":"echo \"$GREETING\"; exit 0","env":{"GREETING":"hi there"}}`, strict))
		requireOK(t, res)
		if got := decodeOut(t, res).StdoutTail; got != "hi there\n" {
			t.Errorf("stdout = %q", got)
		}
	})

	t.Run("non-zero exit", func(t *testing.T) {
		res := exec(t, o.caseFor("fail", `{"command":"echo boom >&2; exit 7"}`, strict))
		se := requireKind(t, res, pluginsdk.KindExitCode)
		if se.ExitCode == nil || *se.ExitCode != 7 || decodeOut(t, res).StderrTail != "boom\n" {
			t.Errorf("error = %+v output = %+v", se, decodeOut(t, res))
		}
	})

	t.Run("pty", func(t *testing.T) {
		res := exec(t, o.caseFor("pty", `{"command":"test -t 1 && echo tty","pty":true}`, strict))
		requireOK(t, res)
		if got := decodeOut(t, res).StdoutTail; got != "tty\n" {
			t.Errorf("stdout = %q", got)
		}
	})

	t.Run("cancel kills the remote process", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		c := o.caseFor("cancel", `{"command":"echo $$ >/tmp/damson-it.pid; exec sleep 120"}`, strict)
		done := make(chan plugintest.Outcome, 1)
		go func() { done <- plugintest.Exec(ctx, t, plug(), c) }()
		// Cancel only once the remote command wrote its pid file.
		pidDeadline := time.Now().Add(20 * time.Second)
		for {
			check := exec(t, o.caseFor("pid", `{"command":"test -s /tmp/damson-it.pid"}`, strict))
			if check.Result.Error == nil && check.Err == nil {
				break
			}
			if time.Now().After(pidDeadline) {
				cancel()
				<-done
				t.Fatal("remote pid file never appeared")
			}
			time.Sleep(200 * time.Millisecond)
		}
		cancel()
		_ = requireKind(t, <-done, pluginsdk.KindCancelled)

		deadline := time.Now().Add(10 * time.Second)
		for {
			check := exec(t, o.caseFor("alive", `{"command":"kill -0 $(cat /tmp/damson-it.pid) 2>/dev/null"}`, strict))
			if check.Result.Error != nil { // kill -0 failed: the process is gone
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("remote process still running after cancel")
			}
			time.Sleep(500 * time.Millisecond)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		res := plugintest.Exec(ctx, t, plug(), o.caseFor("timeout", `{"command":"sleep 120"}`, strict))
		_ = requireKind(t, res, pluginsdk.KindTimeout)
	})

	t.Run("strict host key mismatch", func(t *testing.T) {
		bad := strict
		bad.HostKey = sshtest.KeyLine(sshtest.NewSigner(t).PublicKey())
		res := exec(t, o.caseFor("mismatch", `{"command":"true"}`, bad))
		se := requireKind(t, res, pluginsdk.KindConnection)
		if !strings.Contains(se.Message, "host key mismatch") {
			t.Errorf("message = %q", se.Message)
		}
	})
}
