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
	"github.com/denyszorinets/damson/plugins/internal/s3test"
	"github.com/denyszorinets/damson/plugins/internal/sshtest"
	"github.com/denyszorinets/damson/plugins/internal/winrmtest"
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
		Config: map[string]string{"allow_plain_ftp": "true"},
	}
	// s3: the access and secret keys are bundle values, so the suite checks they never leak.
	s3srv := s3test.Start(t, "contract-bucket")
	s3Case := plugintest.Case{
		Name: "s3",
		Input: json.RawMessage(fmt.Sprintf(`{"mode":"copy","overwrite":true,"source":{"node":{"name":%q,"protocol":"sftp","path":%q}},"destination":{"s3":{"connection":"secret://s3/conn","bucket":"contract-bucket","key":"obj"}}}`,
			sshtest.NodeName, src)),
		Nodes:  map[string]pluginsdk.NodeInfo{sshtest.NodeName: f.Node},
		Bundle: map[string]map[string]string{sshtest.CredentialRef: f.Bundle[sshtest.CredentialRef], "secret://s3/conn": s3srv.Connection()},
	}
	// winrm: the password is a bundle value, so the suite checks it never leaks.
	winSrv := winrmtest.Start(t, winrmtest.Options{User: "svc", Password: "contract-winrm-PASSWORD"})
	winNode := pluginsdk.NodeInfo{Name: "win1", Address: winSrv.Host(), Port: winSrv.Port(), Transport: "winrm", OS: "windows", Credential: "secret://win/cred"}
	winCase := func(name, body string) plugintest.Case {
		return plugintest.Case{
			Name: name, Input: json.RawMessage(body), Config: map[string]string{"tls_ca_pem": winSrv.CAPEM},
			Nodes:  map[string]pluginsdk.NodeInfo{"win1": winNode},
			Bundle: map[string]map[string]string{"secret://win/cred": {"username": "svc", "password": "contract-winrm-PASSWORD"}},
		}
	}
	winBlocking := winCase("blocking", `{"node":"win1","auth":"basic","command":"block"}`)
	// An endless source keeps the transfer running until it is cancelled.
	transferBlocking := transferCase("blocking", "/dev/zero", filepath.Join(dir, "never"))
	return map[string]plugintest.Options{
		"winrm": {
			Cases:    []plugintest.Case{winCase("command", `{"node":"win1","auth":"basic","command":"echo hello"}`)},
			Blocking: &winBlocking,
		},
		"transfer": {
			Cases:    []plugintest.Case{transferCase("copy", src, filepath.Join(dir, "dst")), ftpCase, s3Case},
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
