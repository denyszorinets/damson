package ansible_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
	"github.com/denyszorinets/damson/plugins/ansible"
	"github.com/denyszorinets/damson/plugins/internal/ansibletest"
)

const (
	sshPassword = "ssh-PASSWORD-9917"
	keyBody     = "-----BEGIN OPENSSH PRIVATE KEY-----\nKEYMATERIAL-4411\n-----END OPENSSH PRIVATE KEY-----"
	vaultSecret = "vault-PASSWORD-2203"
	tokenSecret = "extra-TOKEN-7781"
)

func plug() pluginsdk.Plugin { return ansible.Plugins()[0] }

type env struct {
	t      *testing.T
	fake   *ansibletest.Fake
	url    string
	commit string
}

func newEnv(t *testing.T) *env {
	url, commit := ansibletest.Repo(t)
	return &env{t: t, fake: ansibletest.Install(t), url: url, commit: commit}
}

func node(name, transport, cred string, tags ...string) pluginsdk.NodeInfo {
	return pluginsdk.NodeInfo{Name: name, Address: "10.1.0." + fmt.Sprint(len(name)), Transport: transport, Credential: cred, HostKeyPolicy: "strict", Tags: tags}
}

func (e *env) caseOf(body map[string]any, nodes ...pluginsdk.NodeInfo) plugintest.Case {
	in := map[string]any{"repo": map[string]any{"url": e.url, "ref": "main"}, "playbook": "site.yml"}
	for k, v := range body {
		in[k] = v
	}
	raw, _ := json.Marshal(in)
	c := plugintest.Case{
		Name: "ansible", Input: raw, Nodes: map[string]pluginsdk.NodeInfo{},
		Bundle: map[string]map[string]string{
			"secret://h/pw":    {"username": "ops", "password": sshPassword, "become_password": "become-PW-5521"},
			"secret://h/key":   {"username": "deploy", "private_key": keyBody},
			"secret://h/win":   {"username": "svc", "password": "win-PASSWORD-3310"},
			"secret://h/vault": {"value": vaultSecret},
		},
		Secrets: map[string]string{"secret://h/token": tokenSecret},
		Config:  map[string]string{ansible.ConfigAllowInsecureGit: "true"},
	}
	for _, n := range nodes {
		c.Nodes[n.Name] = n
	}
	return c
}

func exec(t *testing.T, c plugintest.Case) plugintest.Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return plugintest.Exec(ctx, t, plug(), c)
}

func decodeOut(t *testing.T, o plugintest.Outcome) ansible.Output {
	t.Helper()
	var out ansible.Output
	if err := json.Unmarshal(o.Result.Outputs, &out); err != nil {
		t.Fatalf("outputs %s: %v", o.Result.Outputs, err)
	}
	return out
}

func requireOK(t *testing.T, o plugintest.Outcome) ansible.Output {
	t.Helper()
	if o.Err != nil || o.Result.Error != nil {
		t.Fatalf("unexpected failure: err=%v step=%v", o.Err, o.Result.Error)
	}
	return decodeOut(t, o)
}

func requireKind(t *testing.T, o plugintest.Outcome, kind pluginsdk.ErrorKind, contains string) {
	t.Helper()
	e := o.Result.Error
	if o.Err != nil || e == nil || e.Kind != kind || !strings.Contains(e.Message, contains) {
		t.Fatalf("step error = %v (err %v); want %s containing %q", e, o.Err, kind, contains)
	}
}

func TestRunsThePlaybookAndReportsTheRecap(t *testing.T) {
	e := newEnv(t)
	e.fake.Stats(t, `{"web1":{"ok":3,"changed":1,"failed":0,"unreachable":0,"skipped":0},"web2":{"ok":3,"changed":0,"failed":0,"unreachable":0,"skipped":1}}`)
	c := e.caseOf(map[string]any{
		"hosts": []string{"web2", "web1"}, "limit": "web*", "tags": []string{"deploy"}, "skip_tags": []string{"slow"},
		"check": true, "diff": true, "forks": 5, "verbosity": 2, "credential": "secret://h/pw",
		"extra_vars":     map[string]any{"version": "1.2", "token": "secret://h/token", "nested": []any{"secret://h/token"}},
		"vault_password": "secret://h/vault",
	}, node("web1", "ssh", "secret://h/key", "web", "prod-eu"), node("web2", "ssh", "", "web", "9lives"))
	out := requireOK(t, exec(t, c))
	if out.Commit != e.commit || out.ExitCode != 0 || strings.Join(out.ChangedHosts, ",") != "web1" || len(out.FailedHosts) != 0 || out.Stats["web2"].Skipped != 1 {
		t.Fatalf("output = %+v", out)
	}

	args := e.fake.Read(t, "args.txt")
	for _, want := range []string{"--limit\nweb*", "--tags\ndeploy", "--skip-tags\nslow", "--check", "--diff", "--forks\n5", "-vv", "--vault-password-file", "-e\n@", "--\nsite.yml"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q:\n%s", want, args)
		}
	}
	// Nothing secret is in the argument list or the environment.
	for _, leak := range []string{sshPassword, keyBody, vaultSecret, tokenSecret, "become-PW"} {
		if strings.Contains(args, leak) || strings.Contains(e.fake.Read(t, "env.txt"), leak) {
			t.Errorf("%q reached the command line or environment", leak)
		}
	}
	env := e.fake.Read(t, "env.txt")
	for _, want := range []string{"ANSIBLE_HOST_KEY_CHECKING=True", "ANSIBLE_CALLBACKS_ENABLED=damson_events", "DAMSON_EVENTS_FILE="} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	if cwd := strings.TrimSpace(e.fake.Read(t, "cwd.txt")); !strings.HasSuffix(cwd, "/repo") {
		t.Errorf("ansible ran in %s; want the checkout", cwd)
	}

	var inv struct {
		All struct {
			Hosts    map[string]map[string]any `json:"hosts"`
			Children map[string]struct {
				Hosts map[string]any `json:"hosts"`
			} `json:"children"`
		} `json:"all"`
	}
	if err := json.Unmarshal([]byte(e.fake.Read(t, "inventory.json")), &inv); err != nil {
		t.Fatal(err)
	}
	w1, w2 := inv.All.Hosts["web1"], inv.All.Hosts["web2"]
	if w1["ansible_user"] != "deploy" || w1["ansible_connection"] != "ssh" || w1["ansible_ssh_private_key_file"] == nil || w1["ansible_password"] != nil {
		t.Errorf("web1 vars = %v", w1)
	}
	if w2["ansible_user"] != "ops" || w2["ansible_password"] != sshPassword || w2["ansible_become_password"] != "become-PW-5521" {
		t.Errorf("web2 vars (default credential) = %v", w2)
	}
	cargs, _ := w1["ansible_ssh_common_args"].(string)
	for _, want := range []string{"HostKeyAlias=web1", "StrictHostKeyChecking=yes", "GlobalKnownHostsFile=/dev/null"} {
		if !strings.Contains(cargs, want) {
			t.Errorf("ssh args lack %s: %s", want, cargs)
		}
	}
	if len(inv.All.Children["web"].Hosts) != 2 || inv.All.Children["prod_eu"].Hosts["web1"] == nil || inv.All.Children["tag_9lives"].Hosts["web2"] == nil {
		t.Errorf("groups = %v", inv.All.Children)
	}
	var vars map[string]any
	if err := json.Unmarshal([]byte(e.fake.Read(t, "extra_vars.json")), &vars); err != nil || vars["token"] != tokenSecret || vars["version"] != "1.2" {
		t.Errorf("extra vars = %v, %v", vars, err)
	}
	if got := strings.TrimSpace(e.fake.Read(t, "vault_password")); got != vaultSecret {
		t.Errorf("vault file = %q", got)
	}
}

func TestCredentialFilesAreRemoved(t *testing.T) {
	e := newEnv(t)
	e.fake.Stats(t, `{"a":{"ok":1,"changed":0,"failed":0,"unreachable":0,"skipped":0}}`)
	c := e.caseOf(map[string]any{"hosts": []string{"a"}}, node("a", "ssh", "secret://h/key"))
	workDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := pluginsdk.ExecRequest{Input: c.Input, Nodes: c.Nodes, WorkDir: workDir, Config: c.Config, Secrets: plugintestResolver(c)}
	res, err := plug().Execute(ctx, req, &plugintest.FakeEmitter{})
	if err != nil || res.Error != nil {
		t.Fatalf("execute = %v, %v", res.Error, err)
	}
	if _, err := os.Stat(workDir + "/creds"); !os.IsNotExist(err) {
		t.Fatalf("the credentials directory is still there: %v", err)
	}
	if !strings.Contains(e.fake.Read(t, "creds-listing.txt"), "inventory.json") {
		t.Error("the run did not see its inventory")
	}
}

type resolver struct {
	bundle  map[string]map[string]string
	secrets map[string]string
}

func (r resolver) Resolve(ref string) (string, error) {
	if v, ok := r.secrets[ref]; ok {
		return v, nil
	}
	if f, ok := r.bundle[ref]; ok {
		return f["value"], nil
	}
	return "", fmt.Errorf("unknown %s", ref)
}

func (r resolver) ResolveFields(ref string) (map[string]string, error) {
	if f, ok := r.bundle[ref]; ok {
		return f, nil
	}
	return nil, fmt.Errorf("unknown %s", ref)
}

func plugintestResolver(c plugintest.Case) pluginsdk.SecretResolver {
	return resolver{bundle: c.Bundle, secrets: c.Secrets}
}

func TestFailuresMapToErrorKinds(t *testing.T) {
	cases := map[string]struct {
		stats string
		code  string
		kind  pluginsdk.ErrorKind
		text  string
	}{
		"failed task":    {`{"a":{"ok":1,"changed":0,"failed":1,"unreachable":0,"skipped":0}}`, "2", pluginsdk.KindExitCode, "failed hosts: a"},
		"unreachable":    {`{"a":{"ok":0,"changed":0,"failed":0,"unreachable":1,"skipped":0}}`, "4", pluginsdk.KindConnection, "unreachable hosts: a"},
		"no recap":       {``, "1", pluginsdk.KindExitCode, "exited with code 1"},
		"mixed failures": {`{"a":{"ok":0,"changed":0,"failed":1,"unreachable":0,"skipped":0},"b":{"ok":0,"changed":0,"failed":0,"unreachable":1,"skipped":0}}`, "4", pluginsdk.KindExitCode, "failed hosts: a"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			if tc.stats != "" {
				e.fake.Stats(t, tc.stats)
			}
			e.fake.ExitCode(t, tc.code)
			o := exec(t, e.caseOf(map[string]any{"hosts": []string{"a", "b"}}, node("a", "ssh", "secret://h/key"), node("b", "ssh", "secret://h/key")))
			requireKind(t, o, tc.kind, tc.text)
			// The step still reports what happened.
			if len(o.Result.Outputs) == 0 {
				t.Error("a failed run returned no outputs")
			}
		})
	}
}

func TestFailsClosedBeforeTheNetwork(t *testing.T) {
	e := newEnv(t)
	for name, tc := range map[string]struct {
		c    plugintest.Case
		kind pluginsdk.ErrorKind
		text string
	}{
		"node not resolved":  {e.caseOf(map[string]any{"hosts": []string{"a", "ghost"}}, node("a", "ssh", "secret://h/key")), pluginsdk.KindValidation, `node "ghost" was not resolved`},
		"no credential":      {e.caseOf(map[string]any{"hosts": []string{"a"}}, node("a", "ssh", "")), pluginsdk.KindAuth, "has no credential"},
		"unknown credential": {e.caseOf(map[string]any{"hosts": []string{"a"}}, node("a", "ssh", "secret://h/nope")), pluginsdk.KindAuth, "resolving credential"},
		"bad transport":      {e.caseOf(map[string]any{"hosts": []string{"a"}}, node("a", "telnet", "secret://h/key")), pluginsdk.KindValidation, "not supported"},
		"insecure policy": {e.caseOf(map[string]any{"hosts": []string{"a"}}, func() pluginsdk.NodeInfo {
			n := node("a", "ssh", "secret://h/key")
			n.HostKeyPolicy = "insecure"
			return n
		}()), pluginsdk.KindValidation, "insecure is not enabled"},
		"bad host name": {e.caseOf(map[string]any{"hosts": []string{"a b"}}, node("a b", "ssh", "secret://h/key")), pluginsdk.KindValidation, "characters ansible hosts cannot use"},
	} {
		t.Run(name, func(t *testing.T) {
			tc.c.Config[ansible.ConfigAllowInsecureGit] = "false" // a clone would fail on the url, so reaching it is visible
			o := exec(t, tc.c)
			requireKind(t, o, tc.kind, tc.text)
			if e.fake.Exists("args.txt") {
				t.Fatal("ansible-playbook ran")
			}
		})
	}
}

func TestEmptyHostListRunsNothing(t *testing.T) {
	e := newEnv(t)
	out := requireOK(t, exec(t, e.caseOf(map[string]any{"hosts": []string{}})))
	if len(out.Stats) != 0 || e.fake.Exists("args.txt") {
		t.Fatalf("output = %+v, ran = %v", out, e.fake.Exists("args.txt"))
	}
}

func TestRequirementsAreInstalledFirst(t *testing.T) {
	e := newEnv(t)
	e.fake.Stats(t, `{"a":{"ok":1,"changed":0,"failed":0,"unreachable":0,"skipped":0}}`)
	requireOK(t, exec(t, e.caseOf(map[string]any{"hosts": []string{"a"}, "requirements": "requirements.yml"}, node("a", "ssh", "secret://h/key"))))
	if got := e.fake.Read(t, "galaxy-args.txt"); !strings.Contains(got, "install\n-r\nrequirements.yml") {
		t.Errorf("galaxy args = %q", got)
	}
	o := exec(t, e.caseOf(map[string]any{"hosts": []string{"a"}, "requirements": "missing.yml"}, node("a", "ssh", "secret://h/key")))
	requireKind(t, o, pluginsdk.KindValidation, "not in the repository")
}

func TestWinRMHostsGetNTLMOverHTTPS(t *testing.T) {
	e := newEnv(t)
	e.fake.Stats(t, `{"w":{"ok":1,"changed":0,"failed":0,"unreachable":0,"skipped":0}}`)
	n := node("w", "winrm", "secret://h/win")
	n.Port = 5986
	c := e.caseOf(map[string]any{"hosts": []string{"w"}}, n)
	c.Config[ansible.ConfigTLSCAPEM] = "-----BEGIN CERTIFICATE-----\nCA\n-----END CERTIFICATE-----"
	requireOK(t, exec(t, c))
	var inv struct {
		All struct {
			Hosts map[string]map[string]any `json:"hosts"`
		} `json:"all"`
	}
	_ = json.Unmarshal([]byte(e.fake.Read(t, "inventory.json")), &inv)
	w := inv.All.Hosts["w"]
	if w["ansible_connection"] != "winrm" || w["ansible_winrm_transport"] != "ntlm" || w["ansible_winrm_scheme"] != "https" ||
		w["ansible_winrm_server_cert_validation"] != "validate" || w["ansible_winrm_ca_trust_path"] == nil || w["ansible_ssh_common_args"] != nil {
		t.Errorf("winrm vars = %v", w)
	}
}

type keys struct {
	mu   sync.Mutex
	seen map[string]string
}

func (k *keys) ReportHostKey(node, key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.seen == nil {
		k.seen = map[string]string{}
	}
	k.seen[node] = key
}

func TestTrustOnFirstUseKeysAreReported(t *testing.T) {
	e := newEnv(t)
	e.fake.Stats(t, `{"new":{"ok":1,"changed":0,"failed":0,"unreachable":0,"skipped":0}}`)
	e.fake.AppendKnownHosts(t, "new ssh-ed25519 AAAAC3NzaFRESH\nother ssh-ed25519 AAAAOTHER\n")
	pinned := node("pinned", "ssh", "secret://h/key")
	pinned.HostKey = "ssh-ed25519 AAAAPINNED"
	fresh := node("new", "ssh", "secret://h/key")
	fresh.HostKeyPolicy = "tofu"
	tofuPinned := node("tp", "ssh", "secret://h/key")
	tofuPinned.HostKeyPolicy, tofuPinned.HostKey = "tofu", "ssh-ed25519 AAAATP"
	c := e.caseOf(map[string]any{"hosts": []string{"pinned", "new", "tp"}}, pinned, fresh, tofuPinned)
	rec := &keys{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := plug().Execute(ctx, pluginsdk.ExecRequest{Input: c.Input, Nodes: c.Nodes, WorkDir: t.TempDir(), Config: c.Config, Secrets: plugintestResolver(c), HostKeys: rec}, &plugintest.FakeEmitter{})
	if err != nil || res.Error != nil {
		t.Fatalf("execute = %v, %v", res.Error, err)
	}
	if len(rec.seen) != 1 || rec.seen["new"] != "ssh-ed25519 AAAAC3NzaFRESH" {
		t.Fatalf("reported keys = %v; want only the new tofu node's", rec.seen)
	}
	var inv struct {
		All struct {
			Hosts map[string]map[string]any `json:"hosts"`
		} `json:"all"`
	}
	_ = json.Unmarshal([]byte(e.fake.Read(t, "inventory.json")), &inv)
	check := func(host, want string) {
		a, _ := inv.All.Hosts[host]["ansible_ssh_common_args"].(string)
		if !strings.Contains(a, "StrictHostKeyChecking="+want+" ") && !strings.HasSuffix(a, "StrictHostKeyChecking="+want) {
			t.Errorf("%s: %s; want StrictHostKeyChecking=%s", host, a, want)
		}
	}
	check("pinned", "yes")
	check("new", "accept-new")
	check("tp", "yes")
}

func TestValidate(t *testing.T) {
	bad := map[string]string{
		`{`: "invalid input",
		`{"repo":{"url":"https://h/r.git"},"playbook":"site.yml"}`:                                "hosts is required",
		`{"repo":{},"playbook":"site.yml","hosts":[]}`:                                            "repo.url",
		`{"repo":{"url":"https://h/r.git"},"hosts":[]}`:                                           "playbook is required",
		`{"repo":{"url":"https://h/r.git"},"playbook":"../x.yml","hosts":[]}`:                     "relative path",
		`{"repo":{"url":"https://h/r.git"},"playbook":"/etc/x.yml","hosts":[]}`:                   "relative path",
		`{"repo":{"url":"https://h/r.git"},"playbook":"-x.yml","hosts":[]}`:                       "relative path",
		`{"repo":{"url":"https://h/r.git"},"playbook":"a.yml","hosts":[],"limit":"a;b"}`:          "limit",
		`{"repo":{"url":"https://h/r.git"},"playbook":"a.yml","hosts":[],"tags":["a b"]}`:         "tag",
		`{"repo":{"url":"https://h/r.git"},"playbook":"a.yml","hosts":[],"forks":500}`:            "forks",
		`{"repo":{"url":"https://h/r.git"},"playbook":"a.yml","hosts":[],"verbosity":9}`:          "verbosity",
		`{"repo":{"url":"https://h/r.git"},"playbook":"a.yml","hosts":[],"extra_vars":{"a-b":1}}`: "variable name",
		`{"repo":{"url":"https://h/r.git"},"playbook":"a.yml","hosts":[],"surprise":1}`:           "invalid input",
	}
	for in, want := range bad {
		err := plug().Validate(context.Background(), json.RawMessage(in))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v; want %q", in, err, want)
		}
	}
	good := `{"repo":{"url":"https://h/r.git"},"playbook":"a/b.yml","hosts":["x"]}`
	if err := plug().Validate(context.Background(), json.RawMessage(good)); err != nil {
		t.Errorf("good input: %v", err)
	}
}

func TestContract(t *testing.T) {
	e := newEnv(t)
	e.fake.Stats(t, `{"a":{"ok":1,"changed":0,"failed":0,"unreachable":0,"skipped":0}}`)
	ok := e.caseOf(map[string]any{"hosts": []string{"a"}, "vault_password": "secret://h/vault", "extra_vars": map[string]any{"t": "secret://h/token"}}, node("a", "ssh", "secret://h/pw"))
	blocking := e.caseOf(map[string]any{"hosts": []string{"a"}, "playbook": "block.yml"}, node("a", "ssh", "secret://h/key"))
	plugintest.RunContract(t, plug(), plugintest.Options{Cases: []plugintest.Case{ok}, Blocking: &blocking, Timeout: 20 * time.Second})
}
