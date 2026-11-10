//go:build unix

package sshtest

import (
	"encoding/json"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
)

// Fixture ties a test server to the node, credential and contract cases a
// plugin test needs.
type Fixture struct {
	Server *Server
	// Node is the inventory node for the server, with a pinned host key and
	// the strict policy.
	Node pluginsdk.NodeInfo
	// Bundle holds the node credential under Node.Credential.
	Bundle map[string]map[string]string
	// PrivateKey and Passphrase are the credential values, for leak checks.
	PrivateKey, Passphrase string
}

// NodeName is the name of the fixture node.
const NodeName = "target"

// CredentialRef is the secret ref of the fixture credential.
const CredentialRef = "secret://ssh/key"

// NewFixture starts a server that accepts a fresh key (encrypted with
// passphrase when it is not empty).
func NewFixture(t testing.TB, passphrase string) *Fixture {
	t.Helper()
	priv, pub := NewClientKey(t, passphrase)
	srv := Start(t, Options{User: "damson", ClientKeys: []ssh.PublicKey{pub}})
	fields := map[string]string{"username": "damson", "private_key": priv}
	if passphrase != "" {
		fields["passphrase"] = passphrase
	}
	return &Fixture{
		Server: srv,
		Node: pluginsdk.NodeInfo{
			Name: NodeName, Address: srv.Host(), Port: srv.Port(), Transport: "ssh", OS: "linux",
			Credential: CredentialRef, HostKey: srv.HostKeyLine(), HostKeyPolicy: "strict",
		},
		Bundle:     map[string]map[string]string{CredentialRef: fields},
		PrivateKey: priv, Passphrase: passphrase,
	}
}

// Case builds a contract case that runs input (a JSON object; "node" is added
// when missing) against the fixture node.
func (f *Fixture) Case(name, input string) plugintest.Case {
	var m map[string]any
	if err := json.Unmarshal([]byte(input), &m); err != nil {
		panic("sshtest: invalid case input: " + err.Error())
	}
	if _, ok := m["node"]; !ok {
		m["node"] = NodeName
	}
	raw, _ := json.Marshal(m)
	return plugintest.Case{
		Name: name, Input: raw,
		Nodes:  map[string]pluginsdk.NodeInfo{NodeName: f.Node},
		Bundle: f.Bundle,
	}
}
