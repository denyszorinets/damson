package buildah_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
	"github.com/denyszorinets/damson/plugins/buildah"
	"github.com/denyszorinets/damson/plugins/internal/buildahtest"
)

const (
	regPassword = "registry-PASSWORD-8841"
	npmSecret   = "npm-TOKEN-5530"
	repoToken   = "repo-TOKEN-1177"
	image       = "registry.test/team/app"
)

func plug() pluginsdk.Plugin { return buildah.Plugins()[0] }

type env struct {
	t      *testing.T
	fake   *buildahtest.Fake
	url    string
	commit string
}

func newEnv(t *testing.T) *env {
	url, commit := buildahtest.Repo(t)
	return &env{t: t, fake: buildahtest.Install(t), url: url, commit: commit}
}

func (e *env) caseOf(body map[string]any) plugintest.Case {
	in := map[string]any{"repo": map[string]any{"url": e.url, "ref": "main"}, "image": image}
	for k, v := range body {
		in[k] = v
	}
	raw, _ := json.Marshal(in)
	return plugintest.Case{
		Name: "buildah", Input: raw,
		Bundle: map[string]map[string]string{
			"secret://reg/cred": {"username": "builder", "password": regPassword},
			"secret://repo/tok": {"token": repoToken},
		},
		Secrets: map[string]string{"secret://ci/npm": npmSecret},
		Config:  map[string]string{buildah.ConfigAllowInsecureGit: "true"},
	}
}

func exec(t *testing.T, c plugintest.Case) plugintest.Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return plugintest.Exec(ctx, t, plug(), c)
}

func decodeOut(t *testing.T, o plugintest.Outcome) buildah.Output {
	t.Helper()
	var out buildah.Output
	if err := json.Unmarshal(o.Result.Outputs, &out); err != nil {
		t.Fatalf("outputs %q: %v", o.Result.Outputs, err)
	}
	return out
}

func requireOK(t *testing.T, o plugintest.Outcome) buildah.Output {
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

func TestBuildsAndPushesWithTheDigest(t *testing.T) {
	e := newEnv(t)
	digest := "sha256:" + strings.Repeat("ab", 32)
	e.fake.Digest(t, digest)
	c := e.caseOf(map[string]any{
		"tags": []string{"1.2", "latest", "1.2"}, "build_args": map[string]string{"VERSION": "1.2"},
		"labels": map[string]string{"org.opencontainers.image.revision": "abc"}, "target": "release", "platform": "linux/arm64",
		"cache":                map[string]any{"from": "registry.test/cache/app", "to": "registry.test/cache/app"},
		"build_secrets":        map[string]string{"npmrc": "secret://ci/npm"},
		"registry_credentials": []map[string]string{{"registry": "registry.test", "credential": "secret://reg/cred"}},
	})
	out := requireOK(t, exec(t, c))
	if out.Image != image || out.Digest != digest || out.ImageRef != image+"@"+digest || strings.Join(out.Tags, ",") != "1.2,latest" || out.Commit != e.commit {
		t.Fatalf("output = %+v", out)
	}

	builds := e.fake.Calls(t, "build")
	if len(builds) != 1 {
		t.Fatalf("builds = %v", builds)
	}
	b := builds[0]
	for _, seq := range [][]string{
		{"build", "--layers", "--tls-verify=true", "-f"}, {"-t", image + ":1.2"}, {"-t", image + ":latest"},
		{"--build-arg", "VERSION=1.2"}, {"--label", "org.opencontainers.image.revision=abc"}, {"--target=release"},
		{"--platform", "linux/arm64"}, {"--cache-from", "registry.test/cache/app"}, {"--cache-to", "registry.test/cache/app"}, {"--"},
	} {
		if !has(b, seq...) {
			t.Errorf("build args lack %v:\n%s", seq, strings.Join(b, " "))
		}
	}
	if !has(b, "--root") || !has(b, "--runroot") {
		t.Error("storage is not confined to the work directory")
	}
	// The Containerfile and the context are absolute paths inside the checkout,
	// so buildah can never read them as a remote URL.
	for i, a := range b {
		if (a == "-f" || a == "--") && (!strings.HasPrefix(b[i+1], "/") || !strings.Contains(b[i+1], "/repo")) {
			t.Errorf("%s %s is not an absolute path in the checkout", a, b[i+1])
		}
	}
	joined := strings.Join(b, "\n")
	if !strings.Contains(joined, "id=npmrc,src=") || strings.Contains(joined, npmSecret) {
		t.Errorf("build secret arguments wrong:\n%s", joined)
	}
	if got := e.fake.Read(t, "secret-copy-npmrc"); got != npmSecret {
		t.Errorf("secret file = %q", got)
	}

	pushes := e.fake.Calls(t, "push")
	if len(pushes) != 2 || !has(pushes[0], "docker://"+image+":1.2") || !has(pushes[1], "docker://"+image+":latest") || !has(pushes[0], "--digestfile") {
		t.Fatalf("pushes = %v", pushes)
	}
	for _, sub := range []string{"rm", "rmi", "unshare"} {
		if len(e.fake.Calls(t, sub)) != 1 {
			t.Errorf("cleanup step %s did not run once", sub)
		}
	}

	// Registry credentials are in the auth file, never on a command line or in the environment.
	var auth struct {
		Auths map[string]struct{ Auth string } `json:"auths"`
	}
	if err := json.Unmarshal([]byte(e.fake.Read(t, "auth.copy")), &auth); err != nil {
		t.Fatal(err)
	}
	want := base64.StdEncoding.EncodeToString([]byte("builder:" + regPassword))
	if auth.Auths["registry.test"].Auth != want {
		t.Errorf("auth file = %+v", auth)
	}
	env := e.fake.CallFile(t, "build", ".env")
	if !strings.Contains(env, "REGISTRY_AUTH_FILE=") {
		t.Error("REGISTRY_AUTH_FILE is not set")
	}
	for _, leak := range []string{regPassword, npmSecret, repoToken} {
		if strings.Contains(joined, leak) || strings.Contains(env, leak) || strings.Contains(strings.Join(pushes[0], " "), leak) {
			t.Errorf("%q reached a command line or the environment", leak)
		}
	}
	if cwd := strings.TrimSpace(e.fake.CallFile(t, "build", ".cwd")); !strings.HasSuffix(cwd, "/repo") {
		t.Errorf("buildah ran in %s", cwd)
	}
}

func TestDefaultTagContainerfileFallbackAndPathOptions(t *testing.T) {
	e := newEnv(t)
	out := requireOK(t, exec(t, e.caseOf(map[string]any{"context": "app"})))
	if len(out.Tags) != 1 || out.Tags[0] != e.commit[:12] {
		t.Fatalf("default tag = %v; want the short commit", out.Tags)
	}
	b := e.fake.Calls(t, "build")[0]
	var cf, cx string
	for i, a := range b {
		switch a {
		case "-f":
			cf = b[i+1]
		case "--":
			cx = b[i+1]
		}
	}
	if !strings.HasSuffix(cf, "/repo/app/Dockerfile") || !strings.HasSuffix(cx, "/repo/app") {
		t.Errorf("the Dockerfile fallback was not used: -f %s, context %s", cf, cx)
	}
	requireOK(t, exec(t, e.caseOf(map[string]any{"containerfile": "custom/Containerfile.x"})))
	o := exec(t, e.caseOf(map[string]any{"containerfile": "nope"}))
	requireKind(t, o, pluginsdk.KindValidation, "not in the repository")
	o = exec(t, e.caseOf(map[string]any{"context": "custom"}))
	requireKind(t, o, pluginsdk.KindValidation, "no Containerfile or Dockerfile")
}

// The context and the Containerfile cannot leave the checkout through a symlink,
// and a context that looks like a remote URL is not one.
func TestBuildPathsStayInsideTheCheckout(t *testing.T) {
	e := newEnv(t)
	for name, tc := range map[string]struct {
		body map[string]any
		text string
	}{
		"context symlink":       {map[string]any{"context": "linkctx"}, "resolves outside the repository"},
		"containerfile symlink": {map[string]any{"context": "evilfile"}, "resolves outside the repository"},
		"explicit symlink":      {map[string]any{"containerfile": "evilfile/Containerfile"}, "resolves outside the repository"},
		"https context":         {map[string]any{"context": "https://example.com/x"}, "not in the repository"},
		"git hosting context":   {map[string]any{"context": "github.com/x/y"}, "not in the repository"},
		"url containerfile":     {map[string]any{"containerfile": "https://example.com/Dockerfile"}, "not in the repository"},
	} {
		t.Run(name, func(t *testing.T) {
			requireKind(t, exec(t, e.caseOf(tc.body)), pluginsdk.KindValidation, tc.text)
			if len(e.fake.Calls(t, "build")) != 0 {
				t.Fatal("buildah ran")
			}
		})
	}
}

// The agent user's own registry logins and caches (HOME, XDG, TMPDIR) are not
// reachable from buildah.
func TestBuildahGetsAPrivateHome(t *testing.T) {
	t.Setenv("HOME", "/home/agentuser")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	t.Setenv("XDG_CONFIG_HOME", "/home/agentuser/.config")
	e := newEnv(t)
	requireOK(t, exec(t, e.caseOf(nil)))
	env := e.fake.CallFile(t, "build", ".env")
	for _, bad := range []string{"/home/agentuser", "/run/user/1000"} {
		if strings.Contains(env, bad) {
			t.Errorf("the agent user's %s reached buildah:\n%s", bad, env)
		}
	}
	for _, want := range []string{"HOME=", "/home\n", "XDG_RUNTIME_DIR=", "BUILDAH_TMPDIR="} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %q", want)
		}
	}
}

// Output of RUN steps in the stderr tail is not read as a registry problem.
func TestRunOutputIsNotClassified(t *testing.T) {
	e := newEnv(t)
	e.fake.Exit(t, "build", "125")
	e.fake.Stderr(t, "build", "dial tcp 10.0.0.1:80: connection refused (from a test in RUN)\nunauthorized: tests need a login\nError: building at STEP \"RUN make test\": exit status 1")
	requireKind(t, exec(t, e.caseOf(nil)), pluginsdk.KindExitCode, "buildah build exited with code 125")
}

func TestPushFalseKeepsTheImageLocal(t *testing.T) {
	e := newEnv(t)
	out := requireOK(t, exec(t, e.caseOf(map[string]any{"push": false})))
	if out.Digest != "" || out.ImageRef != "" || len(e.fake.Calls(t, "push")) != 0 || len(out.Tags) == 0 {
		t.Fatalf("output = %+v, pushes = %v", out, e.fake.Calls(t, "push"))
	}
}

func TestTLSVerifyOffNeedsTheAgentSetting(t *testing.T) {
	e := newEnv(t)
	o := exec(t, e.caseOf(map[string]any{"tls_verify": false}))
	requireKind(t, o, pluginsdk.KindValidation, buildah.ConfigAllowInsecureRegistry)
	if len(e.fake.Calls(t, "build")) != 0 {
		t.Fatal("buildah ran")
	}
	c := e.caseOf(map[string]any{"tls_verify": false})
	c.Config[buildah.ConfigAllowInsecureRegistry] = "true"
	requireOK(t, exec(t, c))
	if !has(e.fake.Calls(t, "build")[0], "--tls-verify=false") || !has(e.fake.Calls(t, "push")[0], "--tls-verify=false") {
		t.Error("--tls-verify=false was not passed")
	}
}

func TestFailuresMapToErrorKinds(t *testing.T) {
	for name, tc := range map[string]struct {
		sub, stderr string
		kind        pluginsdk.ErrorKind
		hasImage    bool
	}{
		"build fails":       {"build", "STEP 3: RUN false", pluginsdk.KindExitCode, true},
		"base image denied": {"build", "Error: initializing source docker://x: unauthorized: authentication required", pluginsdk.KindAuth, true},
		"push denied":       {"push", "Error: trying to reuse blob: requested access to the resource is denied", pluginsdk.KindAuth, true},
		"registry down":     {"push", `Error: dial tcp: lookup registry.test: no such host`, pluginsdk.KindConnection, true},
		"push fails":        {"push", "Error: something else", pluginsdk.KindExitCode, true},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.fake.Exit(t, tc.sub, "125")
			e.fake.Stderr(t, tc.sub, tc.stderr)
			o := exec(t, e.caseOf(nil))
			requireKind(t, o, tc.kind, "buildah "+tc.sub)
			out := decodeOut(t, o)
			if out.Digest != "" || out.Image != image {
				t.Errorf("a failed %s returned %+v", tc.sub, out)
			}
			if len(e.fake.Calls(t, "rmi")) != 1 || len(e.fake.Calls(t, "unshare")) != 1 {
				t.Error("local images were not removed after a failure")
			}
		})
	}
}

func TestInvalidDigestIsAnInternalError(t *testing.T) {
	e := newEnv(t)
	e.fake.Digest(t, "not-a-digest")
	requireKind(t, exec(t, e.caseOf(nil)), pluginsdk.KindInternal, "no valid digest")
}

func TestMissingCredentialsFailBeforeTheClone(t *testing.T) {
	e := newEnv(t)
	c := e.caseOf(map[string]any{"registry_credentials": []map[string]string{{"registry": "registry.test", "credential": "secret://reg/missing"}}})
	c.Config[buildah.ConfigAllowInsecureGit] = "false" // reaching the clone would fail on the url
	requireKind(t, exec(t, c), pluginsdk.KindAuth, "resolving credential")
	c = e.caseOf(map[string]any{"build_secrets": map[string]string{"x": "secret://ci/missing"}})
	c.Config[buildah.ConfigAllowInsecureGit] = "false"
	requireKind(t, exec(t, c), pluginsdk.KindAuth, "resolving build secret")
	if len(e.fake.Calls(t, "build")) != 0 {
		t.Fatal("buildah ran")
	}
}

func TestAgentEnvironmentDoesNotReachBuildah(t *testing.T) {
	t.Setenv("DAMSON_AGENT__TOKEN", "agent-TOKEN-6602")
	t.Setenv("BUILDAH_ISOLATION", "chroot")
	e := newEnv(t)
	requireOK(t, exec(t, e.caseOf(nil)))
	env := e.fake.CallFile(t, "build", ".env")
	if strings.Contains(env, "6602") {
		t.Errorf("the agent token leaked into buildah:\n%s", env)
	}
	if !strings.Contains(env, "BUILDAH_ISOLATION=chroot") {
		t.Error("operator BUILDAH_* settings must pass through")
	}
}

func TestValidate(t *testing.T) {
	ok := func(extra string) string {
		return `{"repo":{"url":"https://h/r.git"},"image":"registry.test/team/app"` + extra + `}`
	}
	bad := map[string]string{
		`{`:                 "invalid input",
		`{"image":"a/b"}`:   "repo.url",
		ok(`,"surprise":1`): "invalid input",
		`{"repo":{"url":"u"},"image":"registry.test/app:1.0"}`:                  "without a scheme, tag or digest",
		`{"repo":{"url":"u"},"image":"registry.test/app@sha256:1"}`:             "without a scheme, tag or digest",
		`{"repo":{"url":"u"},"image":"docker://a/b"}`:                           "without a scheme, tag or digest",
		`{"repo":{"url":"u"},"image":"-a/b"}`:                                   "image",
		`{"repo":{"url":"u"},"image":"Registry/App"}`:                           "image",
		`{"repo":{"url":"u"},"image":""}`:                                       "image",
		ok(`,"tags":["a b"]`):                                                   "tag",
		ok(`,"context":"../x"`):                                                 "relative path",
		ok(`,"containerfile":"/etc/x"`):                                         "relative path",
		ok(`,"build_args":{"A":"secret://x/y"}`):                                "build_secrets",
		ok(`,"build_args":{"a-b":"1"}`):                                         "build_args name",
		ok(`,"labels":{"A":"1"}`):                                               "label",
		ok(`,"platform":"linux"`):                                               "platform",
		ok(`,"target":"a b"`):                                                   "target",
		ok(`,"build_secrets":{"x":"literal"}`):                                  "secret:// ref",
		ok(`,"registry_credentials":[{"registry":"r","credential":"literal"}]`): "registry_credentials",
		ok(`,"registry_credentials":[{"registry":"r.t","credential":"secret://a/b"},{"registry":"r.t","credential":"secret://a/c"}]`): "registry_credentials",
		ok(`,"cache":{"from":"a b"}`): "cache",
	}
	for in, want := range bad {
		err := plug().Validate(context.Background(), json.RawMessage(in))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v; want %q", in, err, want)
		}
	}
	for _, good := range []string{
		ok(``), ok(`,"image":"localhost:5000/app"`), ok(`,"context":"."`), ok(`,"tags":["1.0","v1_2-rc.1"]`),
		`{"repo":{"url":"u"},"image":"registry.internal:5000/team/app"}`,
	} {
		if err := plug().Validate(context.Background(), json.RawMessage(good)); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
}

func TestContract(t *testing.T) {
	e := newEnv(t)
	ok := e.caseOf(map[string]any{
		"build_secrets":        map[string]string{"npmrc": "secret://ci/npm"},
		"registry_credentials": []map[string]string{{"registry": "registry.test", "credential": "secret://reg/cred"}},
		"repo":                 map[string]any{"url": e.url, "ref": "main"},
	})
	blocking := e.caseOf(map[string]any{"tags": []string{"block"}})
	plugintest.RunContract(t, plug(), plugintest.Options{Cases: []plugintest.Case{ok}, Blocking: &blocking, Timeout: 20 * time.Second})
}
