package helm_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
	"github.com/denyszorinets/damson/plugins/helm"
	"github.com/denyszorinets/damson/plugins/internal/helmtest"
)

const (
	kubeSecret = "apiVersion: v1\nkind: Config\n# KUBE-SECRET-4410\n"
	dbPassword = "db-PASSWORD-7312"
	regPass    = "registry-PASSWORD-2290"
)

func plug() pluginsdk.Plugin { return helm.Plugins()[0] }

type env struct {
	t    *testing.T
	fake *helmtest.Fake
}

func newEnv(t *testing.T) *env { return &env{t: t, fake: helmtest.Install(t)} }

func (e *env) caseOf(body map[string]any) plugintest.Case {
	in := map[string]any{
		"release": "app", "namespace": "team-app",
		"chart":   map[string]any{"oci": "oci://registry.test/charts/app", "version": "2.3.0"},
		"cluster": map[string]any{"kubeconfig": "secret://team/kube#kubeconfig"},
	}
	for k, v := range body {
		if v == nil {
			delete(in, k)
			continue
		}
		in[k] = v
	}
	raw, _ := json.Marshal(in)
	return plugintest.Case{
		Name: "helm", Input: raw,
		Bundle: map[string]map[string]string{
			"secret://team/kube":    {"kubeconfig": kubeSecret},
			"secret://team/reg":     {"username": "puller", "password": regPass},
			"secret://team/kubeall": {"kubeconfig": kubeSecret, "other": "x"},
		},
		Secrets: map[string]string{"secret://team/db": dbPassword},
		Config:  map[string]string{helm.ConfigAllowInsecureGit: "true"},
	}
}

func exec(t *testing.T, c plugintest.Case) plugintest.Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return plugintest.Exec(ctx, t, plug(), c)
}

func decodeOut(t *testing.T, o plugintest.Outcome) helm.Output {
	t.Helper()
	var out helm.Output
	if err := json.Unmarshal(o.Result.Outputs, &out); err != nil {
		t.Fatalf("outputs %q: %v", o.Result.Outputs, err)
	}
	return out
}

func requireOK(t *testing.T, o plugintest.Outcome) helm.Output {
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

func has(args []string, seq ...string) bool {
	for i := 0; i+len(seq) <= len(args); i++ {
		if strings.Join(args[i:i+len(seq)], "\x00") == strings.Join(seq, "\x00") {
			return true
		}
	}
	return false
}

func TestUpgradeFromAnOCIChart(t *testing.T) {
	e := newEnv(t)
	e.fake.Stdout(t, "get", helmtest.Release("app", "team-app", 4, "deployed"))
	c := e.caseOf(map[string]any{
		"create_namespace": true, "timeout": "5m", "cluster": map[string]any{"kubeconfig": "secret://team/kubeall", "context": "prod"},
		"values":               map[string]any{"image": map[string]any{"digest": "sha256:abc"}, "db": map[string]any{"password": "secret://team/db"}},
		"set":                  map[string]string{"a.b": "x,y", "env": `back\slash`},
		"registry_credentials": []map[string]string{{"registry": "registry.test", "credential": "secret://team/reg"}},
	})
	out := requireOK(t, exec(t, c))
	if out.Release != "app" || out.Namespace != "team-app" || out.Revision != 4 || out.Status != "deployed" || out.Chart != "app" || out.ChartVersion != "2.3.0" || out.AppVersion != "1.9.1" {
		t.Fatalf("output = %+v", out)
	}
	call := e.fake.Calls(t, "upgrade")[0]
	for _, seq := range [][]string{
		{"upgrade", "--install", "app", "oci://registry.test/charts/app", "--version", "2.3.0"}, {"--namespace", "team-app"},
		{"--kube-context", "prod"}, {"--create-namespace"}, {"--atomic"}, {"--wait"}, {"--timeout", "5m"},
		{`--set-string=a.b=x\,y`}, {`--set-string=env=back\\slash`}, {"--kubeconfig"}, {"--values"}, {"--registry-config"},
	} {
		if !has(call, seq...) {
			t.Errorf("args lack %v:\n%s", seq, strings.Join(call, " "))
		}
	}
	// Secrets are in files only.
	joined := strings.Join(call, "\n")
	envText := e.fake.CallEnv(t, "upgrade")
	for _, leak := range []string{kubeSecret, "KUBE-SECRET", dbPassword, regPass} {
		if strings.Contains(joined, leak) || strings.Contains(envText, leak) {
			t.Errorf("%q reached the command line or the environment", leak)
		}
	}
	if got := e.fake.Read(t, "kubeconfig.copy"); got != kubeSecret {
		t.Errorf("kubeconfig file = %q", got)
	}
	var vals map[string]any
	if err := json.Unmarshal([]byte(e.fake.Read(t, "values-copy-values.json")), &vals); err != nil || vals["db"].(map[string]any)["password"] != dbPassword {
		t.Errorf("values file = %v, %v", vals, err)
	}
	var reg struct {
		Auths map[string]struct{ Auth string } `json:"auths"`
	}
	if err := json.Unmarshal([]byte(e.fake.Read(t, "registry.copy")), &reg); err != nil || reg.Auths["registry.test"].Auth != base64.StdEncoding.EncodeToString([]byte("puller:"+regPass)) {
		t.Errorf("registry config = %+v, %v", reg, err)
	}
}

func TestAtomicAndWaitCanBeTurnedOff(t *testing.T) {
	e := newEnv(t)
	e.fake.Stdout(t, "get", helmtest.Release("app", "team-app", 1, "deployed"))
	requireOK(t, exec(t, e.caseOf(map[string]any{"atomic": false, "wait": false})))
	call := e.fake.Calls(t, "upgrade")[0]
	if has(call, "--atomic") || has(call, "--wait") {
		t.Errorf("args = %v", call)
	}
	if !has(call, "--timeout", "10m") {
		t.Errorf("the default timeout is 10m: %v", call)
	}
}

func TestRepoChartAndEnvironment(t *testing.T) {
	t.Setenv("KUBECONFIG", "/home/agent/.kube/config")
	t.Setenv("DAMSON_AGENT__TOKEN", "agent-TOKEN-6602")
	t.Setenv("HOME", "/home/agentuser")
	e := newEnv(t)
	e.fake.Stdout(t, "get", helmtest.Release("app", "team-app", 2, "deployed"))
	requireOK(t, exec(t, e.caseOf(map[string]any{"chart": map[string]any{"repo": map[string]any{"url": "https://charts.test", "name": "app", "version": "1.0.0"}}})))
	call := e.fake.Calls(t, "upgrade")[0]
	if !has(call, "upgrade", "--install", "app", "app", "--repo", "https://charts.test", "--version", "1.0.0") {
		t.Errorf("args = %v", call)
	}
	envText := e.fake.CallEnv(t, "upgrade")
	for _, bad := range []string{"/home/agent/.kube/config", "6602", "/home/agentuser"} {
		if strings.Contains(envText, bad) {
			t.Errorf("%q reached helm:\n%s", bad, envText)
		}
	}
	for _, want := range []string{"HELM_CACHE_HOME=", "HELM_CONFIG_HOME=", "HELM_DATA_HOME=", "HELM_REGISTRY_CONFIG="} {
		if !strings.Contains(envText, want) {
			t.Errorf("env lacks %s", want)
		}
	}
}

func TestGitChartAndValuesFiles(t *testing.T) {
	e := newEnv(t)
	url := helmtest.Repo(t)
	e.fake.Stdout(t, "get", helmtest.Release("app", "team-app", 1, "deployed"))
	chart := func(path string) map[string]any {
		return map[string]any{"git": map[string]any{"url": url, "ref": "main", "path": path}}
	}
	requireOK(t, exec(t, e.caseOf(map[string]any{"chart": chart("charts/app"), "values_files": []string{"values-prod.yaml", "../../shared/common.yaml"}})))
	call := e.fake.Calls(t, "upgrade")[0]
	if !strings.HasSuffix(call[3], "/chart/charts/app") || !strings.HasPrefix(call[3], "/") {
		t.Errorf("chart argument = %q; want the absolute checkout path", call[3])
	}
	var vf string
	for i, a := range call {
		if a == "--values" && strings.HasSuffix(call[i+1], "values-prod.yaml") {
			vf = call[i+1]
		}
	}
	if vf == "" || !strings.Contains(vf, "/chart/charts/app/") {
		t.Errorf("values file argument missing: %v", call)
	}

	for name, tc := range map[string]struct {
		body map[string]any
		text string
	}{
		"no Chart.yaml":     {map[string]any{"chart": chart("charts/nochart")}, "no Chart.yaml"},
		"symlinked chart":   {map[string]any{"chart": chart("linkchart")}, "resolves outside the repository"},
		"symlinked values":  {map[string]any{"chart": chart("charts/app"), "values_files": []string{"../../shared/values-evil.yaml"}}, "resolves outside the repository"},
		"missing values":    {map[string]any{"chart": chart("charts/app"), "values_files": []string{"nope.yaml"}}, "not in the repository"},
		"missing chart dir": {map[string]any{"chart": chart("charts/zzz")}, "not in the repository"},
		"chart symlink out": {map[string]any{"chart": chart("charts/leaky")}, "symlink"},
	} {
		t.Run(name, func(t *testing.T) {
			before := len(e.fake.Calls(t, "upgrade"))
			requireKind(t, exec(t, e.caseOf(tc.body)), pluginsdk.KindValidation, tc.text)
			if len(e.fake.Calls(t, "upgrade")) != before {
				t.Fatal("helm ran")
			}
		})
	}
}

func TestRollbackFollowsUpWithStatus(t *testing.T) {
	e := newEnv(t)
	e.fake.Stdout(t, "get", helmtest.Release("app", "team-app", 6, "deployed"))
	out := requireOK(t, exec(t, e.caseOf(map[string]any{"action": "rollback", "chart": nil, "rollback_revision": 5})))
	if out.Revision != 6 || out.Status != "deployed" {
		t.Fatalf("output = %+v", out)
	}
	rb := e.fake.Calls(t, "rollback")[0]
	if !has(rb, "rollback", "app", "5") || !has(rb, "--wait") {
		t.Errorf("rollback args = %v", rb)
	}
	if st := e.fake.Calls(t, "get"); len(st) != 1 || !has(st[0], "get", "metadata", "app") || !has(st[0], "--output", "json") {
		t.Errorf("metadata calls = %v", st)
	}
	// Without a revision helm picks the previous one.
	requireOK(t, exec(t, e.caseOf(map[string]any{"action": "rollback", "chart": nil})))
	if rb := e.fake.Calls(t, "rollback")[1]; has(rb, "5") || rb[2] != "--namespace" {
		t.Errorf("rollback args without a revision = %v", rb)
	}
}

func TestStatusAndUninstall(t *testing.T) {
	e := newEnv(t)
	e.fake.Stdout(t, "get", helmtest.Release("app", "team-app", 3, "failed"))
	out := requireOK(t, exec(t, e.caseOf(map[string]any{"action": "status", "chart": nil})))
	if out.Status != "failed" || out.Revision != 3 {
		t.Fatalf("status output = %+v", out)
	}
	out = requireOK(t, exec(t, e.caseOf(map[string]any{"action": "uninstall", "chart": nil})))
	if out.Status != "uninstalled" || out.Release != "app" || len(e.fake.Calls(t, "uninstall")) != 1 {
		t.Fatalf("uninstall output = %+v", out)
	}
}

func TestInClusterIsGated(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.96.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "443")
	e := newEnv(t)
	e.fake.Stdout(t, "get", helmtest.Release("app", "team-app", 1, "deployed"))
	c := e.caseOf(map[string]any{"cluster": map[string]any{"in_cluster": true}})
	requireKind(t, exec(t, c), pluginsdk.KindValidation, helm.ConfigAllowInCluster)
	if len(e.fake.Calls(t, "upgrade")) != 0 {
		t.Fatal("helm ran")
	}
	c.Config[helm.ConfigAllowInCluster] = "true"
	requireOK(t, exec(t, c))
	call := e.fake.Calls(t, "upgrade")[0]
	if has(call, "--kubeconfig") {
		t.Error("in_cluster must not pass a kubeconfig")
	}
	if envText := e.fake.CallEnv(t, "upgrade"); !strings.Contains(envText, "KUBERNETES_SERVICE_HOST=10.96.0.1") {
		t.Error("the in-cluster API server address is not passed")
	}
}

func TestFailuresMapToErrorKinds(t *testing.T) {
	for name, tc := range map[string]struct {
		stderr string
		kind   pluginsdk.ErrorKind
	}{
		"failed upgrade":   {"Error: UPGRADE FAILED: another operation is in progress", pluginsdk.KindExitCode},
		"unauthorized":     {"Error: Kubernetes cluster unreachable: Unauthorized", pluginsdk.KindAuth},
		"forbidden":        {`Error: query: failed to query with labels: secrets is forbidden: User "x" cannot list`, pluginsdk.KindAuth},
		"unreachable":      {"Error: Kubernetes cluster unreachable: Get \"https://k\": dial tcp: no such host", pluginsdk.KindConnection},
		"not ready":        {"Error: UPGRADE FAILED: release app failed, and has been rolled back due to atomic being set: context deadline exceeded", pluginsdk.KindTimeout},
		"output of a hook": {"connection refused (from a hook)\nError: UPGRADE FAILED: boom", pluginsdk.KindExitCode},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.fake.Exit(t, "upgrade", "1")
			e.fake.Stderr(t, "upgrade", tc.stderr)
			o := exec(t, e.caseOf(nil))
			requireKind(t, o, tc.kind, "helm upgrade exited with code 1")
			if len(o.Result.Outputs) != 0 {
				t.Errorf("a failed upgrade returned outputs %s", o.Result.Outputs)
			}
		})
	}
}

func TestUnparsableOutputIsAnInternalError(t *testing.T) {
	e := newEnv(t)
	e.fake.Stdout(t, "get", "Release \"app\" has been upgraded. Happy Helming!")
	requireKind(t, exec(t, e.caseOf(nil)), pluginsdk.KindInternal, "no valid JSON")
}

// helm's own output can be huge (it prints the manifest and notes) and is never
// parsed: a large upgrade output does not turn a deploy into a failure, and what
// `helm get metadata` prints beyond the known fields is not returned.
func TestLargeUpgradeOutputAndExtraMetadataFields(t *testing.T) {
	e := newEnv(t)
	e.fake.Stdout(t, "upgrade", strings.Repeat("x", 600_000)+"\nNOTES: all good\n")
	e.fake.Stdout(t, "get", strings.Replace(helmtest.Release("app", "team-app", 2, "deployed"), "}", `,"config":{"db":"`+dbPassword+`"},"manifest":"secret: `+dbPassword+`"}`, 1))
	o := exec(t, e.caseOf(nil))
	out := requireOK(t, o)
	if out.Revision != 2 {
		t.Fatalf("output = %+v", out)
	}
	if strings.Contains(string(o.Result.Outputs), dbPassword) || strings.Contains(o.Emitter.Text(), dbPassword) {
		t.Error("a value from helm's release reached the outputs or the log")
	}
}

func TestKubeconfigWithFileReferencesOrPluginsIsRefused(t *testing.T) {
	e := newEnv(t)
	for name, kc := range map[string]string{
		"tokenFile":     "users:\n- name: u\n  user:\n    tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token\n",
		"exec":          "users:\n- name: u\n  user:\n    exec:\n      command: /bin/sh\n",
		"auth-provider": "users:\n- name: u\n  user:\n    auth-provider:\n      name: gcp\n",
		"ca file":       "clusters:\n- name: c\n  cluster:\n    certificate-authority: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt\n",
		"client key":    "users:\n- name: u\n  user:\n    client-key: /etc/x\n",
		"client cert":   "users:\n- name: u\n  user:\n    client-certificate: /etc/x\n",
		"not a mapping": "- a\n- b\n",
		"not yaml":      "a: [unclosed",
	} {
		t.Run(name, func(t *testing.T) {
			c := e.caseOf(nil)
			c.Bundle["secret://team/kube"] = map[string]string{"kubeconfig": kc}
			requireKind(t, exec(t, c), pluginsdk.KindValidation, "kubeconfig")
			if len(e.fake.Calls(t, "upgrade")) != 0 {
				t.Fatal("helm ran")
			}
		})
	}
	// The inline forms are fine.
	e.fake.Stdout(t, "get", helmtest.Release("app", "team-app", 1, "deployed"))
	c := e.caseOf(nil)
	c.Bundle["secret://team/kube"] = map[string]string{"kubeconfig": "clusters:\n- name: c\n  cluster:\n    server: https://k.test\n    certificate-authority-data: QQ==\nusers:\n- name: u\n  user:\n    token: abc\n"}
	requireOK(t, exec(t, c))
}

func TestMissingSecretsFailBeforeHelm(t *testing.T) {
	e := newEnv(t)
	for name, body := range map[string]map[string]any{
		"kubeconfig":   {"cluster": map[string]any{"kubeconfig": "secret://team/none#kubeconfig"}},
		"no field":     {"cluster": map[string]any{"kubeconfig": "secret://team/reg"}},
		"value secret": {"values": map[string]any{"x": "secret://team/none"}},
		"registry":     {"registry_credentials": []map[string]string{{"registry": "registry.test", "credential": "secret://team/none"}}},
	} {
		t.Run(name, func(t *testing.T) {
			requireKind(t, exec(t, e.caseOf(body)), pluginsdk.KindAuth, "")
			if len(e.fake.Calls(t, "upgrade")) != 0 {
				t.Fatal("helm ran")
			}
		})
	}
}

func TestValidate(t *testing.T) {
	base := func(extra string) string {
		return `{"release":"app","namespace":"ns","cluster":{"kubeconfig":"secret://a/b#kubeconfig"},"chart":{"oci":"oci://r.test/c/app"}` + extra + `}`
	}
	bad := map[string]string{
		`{`: "invalid input",
		`{"namespace":"ns","cluster":{"in_cluster":true}}`:                                                                                   "release",
		`{"release":"App","namespace":"ns","cluster":{"in_cluster":true}}`:                                                                   "release",
		`{"release":"app","namespace":"N S","cluster":{"in_cluster":true}}`:                                                                  "namespace",
		`{"release":"app","namespace":"ns"}`:                                                                                                 "cluster",
		`{"release":"app","namespace":"ns","cluster":{}}`:                                                                                    "exactly one",
		`{"release":"app","namespace":"ns","cluster":{"in_cluster":true,"kubeconfig":"secret://a/b#k"}}`:                                     "exactly one",
		`{"release":"app","namespace":"ns","cluster":{"kubeconfig":"literal"}}`:                                                              "secret:// ref",
		`{"release":"app","namespace":"ns","cluster":{"in_cluster":true,"context":"x"}}`:                                                     "context",
		`{"release":"app","namespace":"ns","cluster":{"in_cluster":true}}`:                                                                   "chart is required",
		base(`,"action":"destroy"`):                                                                                                          "action must be",
		`{"release":"app","namespace":"ns","cluster":{"in_cluster":true},"chart":{}}`:                                                        "exactly one of oci, repo and git",
		`{"release":"app","namespace":"ns","cluster":{"in_cluster":true},"chart":{"oci":"oci://r/c","repo":{"url":"https://h","name":"a"}}}`: "exactly one of oci, repo and git",
		`{"release":"app","namespace":"ns","cluster":{"in_cluster":true},"chart":{"oci":"https://r/c"}}`:                                     "chart.oci",
		`{"release":"app","namespace":"ns","cluster":{"in_cluster":true},"chart":{"oci":"oci://r/c:1.0"}}`:                                   "chart.oci",
		`{"release":"app","namespace":"ns","cluster":{"in_cluster":true},"chart":{"repo":{"url":"http://h","name":"a"}}}`:                    "chart.repo",
		`{"release":"app","namespace":"ns","cluster":{"in_cluster":true},"chart":{"git":{"url":"u","path":"../x"}}}`:                         "relative path",
		base(`,"timeout":"ten"`):                                            "timeout",
		base(`,"set":{"a":"secret://x/y"}`):                                 "visible on the command line",
		base(`,"set":{"a b":"1"}`):                                          "set key",
		base(`,"values_files":["a.yaml"]`):                                  "only for a git chart source",
		base(`,"rollback_revision":2`):                                      "needs action rollback",
		base(`,"registry_credentials":[{"registry":"r","credential":"x"}]`): "registry_credentials",
		`{"release":"app","namespace":"ns","cluster":{"in_cluster":true},"action":"status","chart":{"oci":"oci://r/c"}}`: "only for action upgrade",
		base(`,"surprise":1`): "invalid input",
	}
	for in, want := range bad {
		err := plug().Validate(context.Background(), json.RawMessage(in))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v; want %q", in, err, want)
		}
	}
	for _, good := range []string{
		base(``),
		`{"release":"app","namespace":"ns","cluster":{"in_cluster":true},"action":"status"}`,
		`{"release":"app","namespace":"ns","cluster":{"in_cluster":true},"action":"rollback","rollback_revision":3}`,
		`{"release":"app","namespace":"ns","cluster":{"in_cluster":true},"chart":{"git":{"url":"https://h/r.git","path":"charts/app"}},"values_files":["v.yaml"]}`,
	} {
		if err := plug().Validate(context.Background(), json.RawMessage(good)); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
}

func TestContract(t *testing.T) {
	e := newEnv(t)
	e.fake.Stdout(t, "get", helmtest.Release("app", "team-app", 1, "deployed"))
	ok := e.caseOf(map[string]any{
		"values":               map[string]any{"db": map[string]any{"password": "secret://team/db"}},
		"registry_credentials": []map[string]string{{"registry": "registry.test", "credential": "secret://team/reg"}},
	})
	blocking := e.caseOf(map[string]any{"release": "block"})
	plugintest.RunContract(t, plug(), plugintest.Options{Cases: []plugintest.Case{ok}, Blocking: &blocking, Timeout: 20 * time.Second})
}
