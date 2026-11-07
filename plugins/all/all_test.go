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
	"github.com/denyszorinets/damson/plugins/internal/ansibletest"
	"github.com/denyszorinets/damson/plugins/internal/buildahtest"
	"github.com/denyszorinets/damson/plugins/internal/ftptest"
	"github.com/denyszorinets/damson/plugins/internal/helmtest"
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
		Config: map[string]string{"allow_plain_s3": "true"},
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
	// ansible: fake ansible binaries and a local repository; the credentials are
	// bundle values, so the suite checks they never leak.
	ansibleURL, _ := ansibletest.Repo(t)
	fake := ansibletest.Install(t)
	fake.Stats(t, `{"web1":{"ok":1,"changed":0,"failed":0,"unreachable":0,"skipped":0}}`)
	ansibleCase := func(name, playbook string) plugintest.Case {
		return plugintest.Case{
			Name:  name,
			Input: json.RawMessage(fmt.Sprintf(`{"repo":{"url":%q,"ref":"main"},"playbook":%q,"hosts":["web1"],"vault_password":"secret://ans/vault"}`, ansibleURL, playbook)),
			Nodes: map[string]pluginsdk.NodeInfo{"web1": {Name: "web1", Address: "10.0.0.1", Transport: "ssh", Credential: "secret://ans/cred", HostKeyPolicy: "strict", Tags: []string{"web"}}},
			Bundle: map[string]map[string]string{
				"secret://ans/cred":  {"username": "ops", "password": "contract-ansible-PASSWORD"},
				"secret://ans/vault": {"value": "contract-vault-PASSWORD"},
			},
			Config: map[string]string{"allow_insecure_git": "true"},
		}
	}
	ansibleBlocking := ansibleCase("blocking", "block.yml")
	// buildah: fake buildah and a local repository; the registry password and
	// the build secret are bundle values, so the suite checks they never leak.
	buildahURL, _ := buildahtest.Repo(t)
	buildahtest.Install(t)
	buildahCase := func(name string, tags string) plugintest.Case {
		return plugintest.Case{
			Name:  name,
			Input: json.RawMessage(fmt.Sprintf(`{"repo":{"url":%q,"ref":"main"},"image":"registry.test/team/app","tags":[%q],"build_secrets":{"npmrc":"secret://b/npm"},"registry_credentials":[{"registry":"registry.test","credential":"secret://b/reg"}]}`, buildahURL, tags)),
			Bundle: map[string]map[string]string{
				"secret://b/reg": {"username": "builder", "password": "contract-registry-PASSWORD"},
				"secret://b/npm": {"value": "contract-npm-TOKEN"},
			},
			Config: map[string]string{"allow_insecure_git": "true"},
		}
	}
	buildahBlocking := buildahCase("blocking", "block")
	// helm: fake helm; the kubeconfig and the registry password are bundle values,
	// so the suite checks they never leak.
	helmFake := helmtest.Install(t)
	helmFake.Stdout(t, "get", helmtest.Release("app", "team-app", 1, "deployed"))
	helmCase := func(name, release string) plugintest.Case {
		return plugintest.Case{
			Name:  name,
			Input: json.RawMessage(fmt.Sprintf(`{"release":%q,"namespace":"team-app","chart":{"oci":"oci://registry.test/charts/app","version":"2.3.0"},"cluster":{"kubeconfig":"secret://h/kube#kubeconfig"},"registry_credentials":[{"registry":"registry.test","credential":"secret://h/reg"}]}`, release)),
			Bundle: map[string]map[string]string{
				"secret://h/reg":  {"username": "puller", "password": "contract-helm-REGISTRY-PW"},
				"secret://h/kube": {"kubeconfig": "apiVersion: v1\nkind: Config\n# contract-KUBECONFIG-secret\n"},
			},
		}
	}
	helmBlocking := helmCase("blocking", "block")
	return map[string]plugintest.Options{
		"helm":    {Cases: []plugintest.Case{helmCase("upgrade", "app")}, Blocking: &helmBlocking},
		"buildah": {Cases: []plugintest.Case{buildahCase("build", "1.0")}, Blocking: &buildahBlocking},
		"ansible": {Cases: []plugintest.Case{ansibleCase("playbook", "site.yml")}, Blocking: &ansibleBlocking},
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
