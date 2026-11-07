package gitcheckout

import (
	"errors"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

func TestSSHCommandIgnoresTheUsersConfigAndKeys(t *testing.T) {
	dir := t.TempDir()
	for name, auth := range map[string]Auth{
		"anonymous":   {},
		"with key":    {SSHKey: "KEY"},
		"known hosts": {KnownHosts: "h ssh-ed25519 AAAA"},
		"insecure":    {InsecureHostKey: true},
	} {
		cmd, err := sshCommand(dir, auth)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, want := range []string{"-F /dev/null", "BatchMode=yes", "IdentitiesOnly=yes", "GlobalKnownHostsFile=/dev/null", "ControlMaster=no"} {
			if !strings.Contains(cmd, want) {
				t.Errorf("%s: %q lacks %q", name, cmd, want)
			}
		}
		if auth.SSHKey == "" && !strings.Contains(cmd, "PubkeyAuthentication=no") {
			t.Errorf("%s: the agent user's default keys could be offered: %q", name, cmd)
		}
		if auth.KnownHosts == "" && !auth.InsecureHostKey && !strings.Contains(cmd, "StrictHostKeyChecking=yes") {
			t.Errorf("%s: must fail closed: %q", name, cmd)
		}
	}
}

func TestEnvironmentSetsSSHForHTTPSReposToo(t *testing.T) {
	env, conf, err := environment(t.TempDir(), Repo{URL: "https://h/r.git"}, Auth{Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(env, "\n"), "GIT_SSH_COMMAND=ssh -F /dev/null") {
		t.Error("a submodule over ssh would use the user's ssh setup")
	}
	if strings.Contains(strings.Join(env, "\n"), "GIT_ASKPASS") || strings.Contains(strings.Join(append(env, conf...), "\n"), "tok") {
		t.Errorf("env/conf leak the token or an askpass: %v %v", env, conf)
	}
	if !strings.Contains(strings.Join(conf, " "), "credential.https://h.helper=") {
		t.Errorf("the helper is not scoped to the host: %v", conf)
	}
}

func TestClassify(t *testing.T) {
	for stderr, want := range map[string]pluginsdk.ErrorKind{
		"fatal: Authentication failed for 'https://h/r.git/'":                                    pluginsdk.KindAuth,
		"git@h: Permission denied (publickey).":                                                  pluginsdk.KindAuth,
		"Host key verification failed.":                                                          pluginsdk.KindAuth,
		"fatal: could not read Username for 'https://h': terminal prompts disabled":              pluginsdk.KindAuth,
		"fatal: unable to access 'https://h/r.git/': Could not resolve host: h":                  pluginsdk.KindConnection,
		"ssh: connect to host h port 22: Connection refused":                                     pluginsdk.KindConnection,
		"fatal: couldn't find remote ref nope":                                                   pluginsdk.KindValidation,
		"fatal: repository 'https://h/r.git/' not found":                                         pluginsdk.KindValidation,
		"error: something unexpected":                                                            pluginsdk.KindExitCode,
		"fatal: remote error: upload-pack: not our ref 0123456789012345678901234567890123456789": pluginsdk.KindValidation,
	} {
		err := classify("fetch", stderr, errors.New("exit status 128"))
		se, ok := err.(*pluginsdk.StepError)
		if !ok || se.Kind != want {
			t.Errorf("%q -> %v, want %s", stderr, err, want)
		}
	}
}
