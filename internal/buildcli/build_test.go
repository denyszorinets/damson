package buildcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestParseWith(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	tests := []struct {
		in   string
		want With
		err  string
	}{
		{"example.com/a", With{Path: "example.com/a", Version: "latest"}, ""},
		{"example.com/a@v1.2.3", With{Path: "example.com/a", Version: "v1.2.3"}, ""},
		{"example.com/a=" + dir, With{Path: "example.com/a", Dir: dir}, ""},
		{"example.com/a@v1.0.0=" + dir, With{}, "cannot be combined"},
		{"example.com/a=" + empty, With{}, "no go.mod"},
		{"-example.com/a", With{}, "invalid module path"},
		{"example.com/../a", With{}, "invalid module path"},
		{"example.com//a", With{}, "invalid module path"},
		{"example.com/a b", With{}, "invalid module path"},
		{"example.com/$(x)", With{}, "invalid module path"},
		{"localpath", With{}, "contain a dot"},
		{"", With{}, "invalid module path"},
		{"example.com/a@-v1", With{}, "invalid version"},
		{"example.com/a@v1;rm", With{}, "invalid version"},
		{DamsonModule + "@v1.0.0", With{}, "--damson-version"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseWith(tc.in)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestParseAllRejectsDuplicatesAndVersionWithReplace(t *testing.T) {
	if _, err := parseAll([]string{"example.com/a", "example.com/a@v1.0.0"}, ""); err == nil {
		t.Error("duplicate module accepted")
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600)
	if _, err := parseAll([]string{DamsonModule + "=" + dir}, "v1.0.0"); err == nil {
		t.Error("--damson-version with a damson replace accepted")
	}
	if _, err := parseAll(nil, "-v1"); err == nil {
		t.Error("bad --damson-version accepted")
	}
}

func TestDamsonVersionFromBuildInfo(t *testing.T) {
	mod := func(path, v string, rep *debug.Module) *debug.Module {
		return &debug.Module{Path: path, Version: v, Replace: rep}
	}
	tests := []struct {
		name string
		bi   *debug.BuildInfo
		want string
		err  bool
	}{
		{"release", &debug.BuildInfo{Main: *mod(DamsonModule, "v1.2.3", nil)}, "v1.2.3", false},
		{"pseudo", &debug.BuildInfo{Main: *mod(DamsonModule, "v0.0.0-20261101000000-abcdef123456", nil)}, "v0.0.0-20261101000000-abcdef123456", false},
		{"devel", &debug.BuildInfo{Main: *mod(DamsonModule, "(devel)", nil)}, "", true},
		{"dirty", &debug.BuildInfo{Main: *mod(DamsonModule, "v1.2.3+dirty", nil)}, "", true},
		{"replaced", &debug.BuildInfo{Main: *mod(DamsonModule, "v1.2.3", &debug.Module{Path: "../x"})}, "", true},
		{"custom binary", &debug.BuildInfo{Main: debug.Module{Path: "damson-custom-build"}, Deps: []*debug.Module{mod("other", "v1", nil), mod(DamsonModule, "v1.4.0", nil)}}, "v1.4.0", false},
		{"custom binary replaced", &debug.BuildInfo{Main: debug.Module{Path: "damson-custom-build"}, Deps: []*debug.Module{mod(DamsonModule, "v0.0.0", &debug.Module{Path: "/x"})}}, "", true},
		{"missing", &debug.BuildInfo{Main: debug.Module{Path: "other"}}, "", true},
		{"nil", nil, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := damsonVersion(tc.bi)
			if (err != nil) != tc.err || got != tc.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestGeneratedMainGolden(t *testing.T) {
	got, err := generateMain([]With{{Path: "example.com/a"}, {Path: "example.com/b/c"}})
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/main.go.golden")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("generated main differs from testdata/main.go.golden:\n%s", got)
	}
}

type call struct {
	dir  string
	env  []string
	args []string
}

// fakeGo records go invocations and answers `go list`; `go build` writes the output.
type fakeGo struct {
	calls   []call
	listAns string
	answers []string // successive answers of go list; listAns when exhausted
	failOn  string
	files   map[string]string // work dir files seen at the first call
}

func (f *fakeGo) run(_ context.Context, dir string, env []string, args ...string) (string, error) {
	f.calls = append(f.calls, call{dir, env, args})
	if f.files == nil {
		f.files = map[string]string{}
		for _, n := range []string{"go.mod", "go.sum", "main.go"} {
			if b, err := os.ReadFile(filepath.Join(dir, n)); err == nil {
				f.files[n] = string(b)
			}
		}
	}
	if args[0] == f.failOn {
		return "", errors.New("exit status 1")
	}
	switch args[0] {
	case "list":
		if len(f.answers) > 0 {
			a := f.answers[0]
			f.answers = f.answers[1:]
			return a + "\n", nil
		}
		return f.listAns + "\n", nil
	case "build":
		for i, a := range args {
			if a == "-o" {
				return "", os.WriteFile(args[i+1], []byte("binary"), 0o755)
			}
		}
	}
	return "", nil
}

func release() *debug.BuildInfo {
	return &debug.BuildInfo{Main: debug.Module{Path: DamsonModule, Version: "v1.2.3"}}
}

func runBuild(t *testing.T, f *fakeGo, environ []string, args ...string) (int, string) {
	t.Helper()
	var errb bytes.Buffer
	code := Run(context.Background(), args, Env{Stdout: &errb, Stderr: &errb, Environ: environ, BuildInfo: release(), Run: f.run})
	return code, errb.String()
}

func TestBuildCommands(t *testing.T) {
	out := filepath.Join(t.TempDir(), "damson-x")
	f := &fakeGo{listAns: "v1.2.3"}
	code, stderr := runBuild(t, f, []string{"PATH=/bin", "GOFLAGS=-mod=vendor"}, "--with", "example.com/a@v1.0.0", "-o", out)
	if code != ExitOK {
		t.Fatalf("code %d: %s", code, stderr)
	}
	var verbs []string
	for _, c := range f.calls {
		verbs = append(verbs, c.args[0])
		env := strings.Join(c.env, " ")
		if !strings.Contains(env, "GOWORK=off") || !strings.Contains(env, "CGO_ENABLED=0") {
			t.Errorf("%s: env = %v", c.args[0], c.env)
		}
		for _, bad := range []string{"GOSUMDB", "GONOSUMDB", "GOINSECURE"} {
			if strings.Contains(env, bad) {
				t.Errorf("%s sets %s", c.args[0], bad)
			}
		}
	}
	if strings.Join(verbs, ",") != "get,list,build,list" {
		t.Fatalf("go commands = %v", verbs)
	}
	get := f.calls[0].args
	if get[2] != "--" || get[3] != DamsonModule+"@v1.2.3" || get[4] != "example.com/a@v1.0.0" {
		t.Errorf("go get args = %v", get)
	}
	for _, c := range f.calls {
		if !strings.Contains(strings.Join(c.args, " "), "-modfile="+filepath.Join(c.dir, "go.mod")) {
			t.Errorf("go %s does not pin -modfile: %v", c.args[0], c.args)
		}
	}
	if !strings.Contains(strings.Join(f.calls[2].args, " "), "-mod=mod") {
		t.Errorf("go build args = %v", f.calls[2].args)
	}
	b := f.calls[2].args
	if !strings.Contains(strings.Join(b, " "), "-ldflags=-X="+DamsonModule+"/internal/version.Version=v1.2.3") {
		t.Errorf("go build args = %v", b)
	}
	if !strings.Contains(f.files["main.go"], `p0 "example.com/a"`) {
		t.Errorf("main.go = %s", f.files["main.go"])
	}
	if got, err := os.ReadFile(out); err != nil || string(got) != "binary" {
		t.Errorf("output = %q, %v", got, err)
	}
	if _, err := os.Stat(f.calls[0].dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("work dir not removed: %v", err)
	}
}

func TestBuildHonorsCGOEnabledAndWarnsAboutWeakenedChecksums(t *testing.T) {
	f := &fakeGo{listAns: "v1.2.3"}
	code, stderr := runBuild(t, f, []string{"CGO_ENABLED=1", "GOSUMDB=off"}, "--with", "example.com/a", "-o", filepath.Join(t.TempDir(), "d"))
	if code != ExitOK || !strings.Contains(stderr, "checksum verification is weakened") {
		t.Fatalf("code %d: %s", code, stderr)
	}
	if env := strings.Join(f.calls[0].env, " "); strings.Contains(env, "CGO_ENABLED=0") || !strings.Contains(env, "CGO_ENABLED=1") {
		t.Errorf("env = %s", env)
	}
}

func TestDamsonUpgradedBySelectionFails(t *testing.T) {
	out := filepath.Join(t.TempDir(), "d")
	f := &fakeGo{listAns: "v1.9.0"}
	code, stderr := runBuild(t, f, nil, "--with", "example.com/a", "-o", out)
	if code != ExitError || !strings.Contains(stderr, "--damson-version v1.9.0") {
		t.Fatalf("code %d: %s", code, stderr)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Error("an output was written")
	}
}

func TestFailedBuildLeavesNoOutputAndHintsTheConvention(t *testing.T) {
	out := filepath.Join(t.TempDir(), "d")
	f := &fakeGo{listAns: "v1.2.3", failOn: "build"}
	code, stderr := runBuild(t, f, nil, "--with", "example.com/a", "-o", out)
	if code != ExitError || strings.Contains(stderr, "wrote") {
		t.Fatalf("code %d: %s", code, stderr)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Error("an output was written")
	}
	if _, err := os.Stat(out + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Error("temporary output left behind")
	}
}

func TestReplacedModulesAreWrittenToGoModAndSeedGoSum(t *testing.T) {
	ext := t.TempDir()
	_ = os.WriteFile(filepath.Join(ext, "go.mod"), []byte("module example.com/a\n"), 0o600)
	_ = os.WriteFile(filepath.Join(ext, "go.sum"), []byte("example.com/dep v1.0.0 h1:abc=\nexample.com/dep v1.0.0 h1:abc=\n"), 0o600)
	dam := t.TempDir()
	_ = os.WriteFile(filepath.Join(dam, "go.mod"), []byte("module "+DamsonModule+"\n"), 0o600)
	f := &fakeGo{}
	code, stderr := runBuild(t, f, nil, "--with", DamsonModule+"="+dam, "--with", "example.com/a="+ext, "-o", filepath.Join(t.TempDir(), "d"))
	if code != ExitOK {
		t.Fatalf("code %d: %s", code, stderr)
	}
	for _, c := range f.calls {
		if c.args[0] == "get" || c.args[0] == "list" {
			t.Errorf("unexpected go %s with only local modules", c.args[0])
		}
	}
	mod := f.files["go.mod"]
	if !strings.Contains(mod, "replace example.com/a => \""+ext+"\"") || !strings.Contains(mod, "replace "+DamsonModule+" =>") {
		t.Errorf("go.mod = %s", mod)
	}
	if strings.Count(f.files["go.sum"], "example.com/dep") != 1 {
		t.Errorf("go.sum = %q", f.files["go.sum"])
	}
	if !strings.Contains(strings.Join(f.calls[len(f.calls)-1].args, " "), "internal/version.Version=dev") {
		t.Errorf("version for a replaced damson should be the running binary's: %v", f.calls[len(f.calls)-1].args)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{{}, {"extra"}, {"--with", "bad"}, {"--bogus"}} {
		f := &fakeGo{}
		if code, _ := runBuild(t, f, nil, args...); code != ExitUsage {
			t.Errorf("%v: code %d", args, code)
		}
		if len(f.calls) != 0 {
			t.Errorf("%v: go was run", args)
		}
	}
}

func TestGoMissingIsAClearError(t *testing.T) {
	var errb bytes.Buffer
	t.Setenv("PATH", t.TempDir())
	code := Run(context.Background(), []string{"--with", "example.com/a", "-o", filepath.Join(t.TempDir(), "d")}, Env{Stdout: &errb, Stderr: &errb, BuildInfo: release()})
	if code != ExitError || !strings.Contains(errb.String(), "go tool is not in PATH") {
		t.Fatalf("code %d: %s", code, errb.String())
	}
}

func TestBuildThatRaisesDamsonIsRejected(t *testing.T) {
	out := filepath.Join(t.TempDir(), "d")
	f := &fakeGo{answers: []string{"v1.2.3", "v1.9.0"}}
	code, stderr := runBuild(t, f, nil, "--with", "example.com/a", "-o", out)
	if code != ExitError || !strings.Contains(stderr, "--damson-version v1.9.0") {
		t.Fatalf("code %d: %s", code, stderr)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Error("an output was written")
	}
}

func TestInstallDoesNotFollowPlantedSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	_ = os.WriteFile(victim, []byte("keep"), 0o600)
	out := filepath.Join(dir, "damson")
	if err := os.Symlink(victim, out+".tmp"); err != nil {
		t.Skip("no symlinks")
	}
	bin := filepath.Join(t.TempDir(), "bin")
	_ = os.WriteFile(bin, []byte("binary"), 0o600)
	if err := install(bin, out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatalf("victim overwritten: %q", b)
	}
	if b, _ := os.ReadFile(out); string(b) != "binary" {
		t.Fatalf("output = %q", b)
	}
	if fi, _ := os.Stat(out); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", fi.Mode())
	}
}

func TestDamsonVersionMustBeSemver(t *testing.T) {
	for _, v := range []string{"latest", "master", "1.2.3", "v1.2"} {
		if _, err := parseAll(nil, v); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
	if _, err := parseAll(nil, "v1.2.3-rc.1+meta"); err != nil {
		t.Error(err)
	}
}
