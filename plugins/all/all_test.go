package all_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
	"github.com/denyszorinets/damson/plugins/all"
	"github.com/denyszorinets/damson/plugins/internal/ftptest"
	"github.com/denyszorinets/damson/plugins/internal/sshtest"
)

// contractOptions maps each built-in plugin name to its contract cases. Add an
// entry here when a built-in is added to all.Plugins.
func contractOptions(t *testing.T) map[string]plugintest.Options {
	f := sshtest.NewFixture(t, "")
	blocking := f.Case("blocking", `{"command":"sleep 30"}`)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	transferCase := func(name, from, to string) plugintest.Case {
		in := fmt.Sprintf(`{"mode":"copy","overwrite":true,"source":{"node":{"name":%q,"protocol":"sftp","path":%q}},"destination":{"node":{"name":%q,"protocol":"sftp","path":%q}}}`,
			sshtest.NodeName, from, sshtest.NodeName, to)
		return plugintest.Case{Name: name, Input: json.RawMessage(in),
			Nodes: map[string]pluginsdk.NodeInfo{sshtest.NodeName: f.Node}, Bundle: f.Bundle}
	}
	// ftp: the password is a bundle value, so the suite checks it never leaks.
	ftpSrv := ftptest.Start(t, ftptest.Options{User: "ftpuser", Password: "contract-ftp-PASSWORD"})
	ftpCase := plugintest.Case{
		Name: "ftp",
		Input: json.RawMessage(fmt.Sprintf(`{"mode":"copy","overwrite":true,"source":{"node":{"name":"ftpnode","protocol":"ftp","path":%q}},"destination":{"node":{"name":"ftpnode","protocol":"ftp","path":%q}}}`,
			src, filepath.Join(dir, "ftp-dst"))),
		Nodes: map[string]pluginsdk.NodeInfo{"ftpnode": {
			Name: "ftpnode", Address: ftpSrv.Host(), Transport: "ssh", Credential: "secret://ftp/cred",
			Attributes: map[string]any{"ftp_port": float64(ftpSrv.Port())},
		}},
		Bundle: map[string]map[string]string{"secret://ftp/cred": {"username": "ftpuser", "password": "contract-ftp-PASSWORD"}},
	}
	// An endless source keeps the transfer running until it is cancelled.
	transferBlocking := transferCase("blocking", "/dev/zero", filepath.Join(dir, "never"))
	return map[string]plugintest.Options{
		"transfer": {
			Cases:    []plugintest.Case{transferCase("copy", src, filepath.Join(dir, "dst")), ftpCase},
			Blocking: &transferBlocking,
		},
		"ssh": {
			Cases:    []plugintest.Case{f.Case("command", `{"command":"echo hello"}`)},
			Blocking: &blocking,
		},
	}
}

func TestBuiltinsPassContract(t *testing.T) {
	plugintest.RunContractAll(t, all.Plugins(), contractOptions(t))
}

func TestSource(t *testing.T) {
	plugintest.CheckSource(t, ".", plugintest.SkipImportRule())
}
