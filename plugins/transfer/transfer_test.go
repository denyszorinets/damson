//go:build unix

package transfer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
	"github.com/denyszorinets/damson/plugins/internal/sshtest"
	"github.com/denyszorinets/damson/plugins/transfer"
)

func plug() pluginsdk.Plugin { return transfer.Plugins()[0] }

// input builds a transfer input between two paths on the fixture node.
func input(mode, src, dst string, extra map[string]any) string {
	m := map[string]any{
		"mode":        mode,
		"source":      map[string]any{"node": map[string]any{"name": sshtest.NodeName, "protocol": "sftp", "path": src}},
		"destination": map[string]any{"node": map[string]any{"name": sshtest.NodeName, "protocol": "sftp", "path": dst}},
	}
	for k, v := range extra {
		m[k] = v
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

func caseOf(f *sshtest.Fixture, name, in string) plugintest.Case {
	return plugintest.Case{
		Name: name, Input: json.RawMessage(in),
		Nodes:  map[string]pluginsdk.NodeInfo{sshtest.NodeName: f.Node},
		Bundle: f.Bundle,
	}
}

func exec(t *testing.T, c plugintest.Case) plugintest.Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return plugintest.Exec(ctx, t, plug(), c)
}

func requireOK(t *testing.T, o plugintest.Outcome) transfer.Output {
	t.Helper()
	if o.Err != nil || o.Result.Error != nil {
		t.Fatalf("unexpected failure: err=%v step=%v", o.Err, o.Result.Error)
	}
	var out transfer.Output
	if err := json.Unmarshal(o.Result.Outputs, &out); err != nil {
		t.Fatalf("outputs %s: %v", o.Result.Outputs, err)
	}
	return out
}

func requireKind(t *testing.T, o plugintest.Outcome, kind pluginsdk.ErrorKind, contains string) {
	t.Helper()
	if o.Err != nil {
		t.Fatalf("unexpected plugin fault: %v", o.Err)
	}
	e := o.Result.Error
	if e == nil || e.Kind != kind || !strings.Contains(e.Message, contains) {
		t.Fatalf("step error = %v; want %s containing %q", e, kind, contains)
	}
}

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func partials(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "*.damson-partial-*"))
	return m
}

func TestContract(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	src := write(t, dir, "contract-src", "hello")
	// An endless source keeps the copy running until the step is cancelled.
	fifo := "/dev/zero"
	blocking := caseOf(f, "blocking", input("copy", fifo, filepath.Join(dir, "never"), nil))
	plugintest.RunContract(t, plug(), plugintest.Options{
		Cases:    []plugintest.Case{caseOf(f, "copy", input("copy", src, filepath.Join(dir, "contract-dst"), map[string]any{"overwrite": true}))},
		Blocking: &blocking,
	})
}

func TestSource(t *testing.T) { plugintest.CheckSource(t, ".") }

func TestCopyWithEveryVerifyLevel(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	content := strings.Repeat("damson ", 20000)
	for _, level := range []string{"", "none", "size", "sha256"} {
		t.Run("verify="+level, func(t *testing.T) {
			dir := t.TempDir()
			src := write(t, dir, "src", content)
			dst := filepath.Join(dir, "dst")
			extra := map[string]any{}
			if level != "" {
				extra["verify"] = level
			}
			out := requireOK(t, exec(t, caseOf(f, "c", input("copy", src, dst, extra))))
			if out.DestinationPath != dst || out.Bytes != int64(len(content)) || out.SHA256 != sum(content) {
				t.Fatalf("output = %+v", out)
			}
			if read(t, dst) != content || read(t, src) != content {
				t.Fatal("content differs or the source changed")
			}
			if p := partials(t, dir); len(p) != 0 {
				t.Fatalf("partial files left: %v", p)
			}
		})
	}
}

func TestDirectoryDestinationAppendsName(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	src := write(t, dir, "report.txt", "x")
	out := requireOK(t, exec(t, caseOf(f, "c", input("copy", src, dir+"/out/", map[string]any{"mkdirs": true}))))
	if out.DestinationPath != dir+"/out/report.txt" || read(t, out.DestinationPath) != "x" {
		t.Fatalf("output = %+v", out)
	}
}

func TestMoveDeletesSourceAfterVerification(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	src := write(t, dir, "src", "payload")
	dst := filepath.Join(dir, "dst")
	requireOK(t, exec(t, caseOf(f, "m", input("move", src, dst, map[string]any{"verify": "sha256"}))))
	if read(t, dst) != "payload" {
		t.Fatal("destination content wrong")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
}

func TestOverwrite(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	src := write(t, dir, "src", "new")
	dst := write(t, dir, "dst", "old")
	o := exec(t, caseOf(f, "c", input("copy", src, dst, nil)))
	requireKind(t, o, pluginsdk.KindValidation, "destination exists")
	if read(t, dst) != "old" || len(partials(t, dir)) != 0 {
		t.Fatal("a refused transfer touched the destination or left a partial")
	}
	requireOK(t, exec(t, caseOf(f, "c", input("copy", src, dst, map[string]any{"overwrite": true}))))
	if read(t, dst) != "new" {
		t.Fatal("overwrite did not replace the file")
	}
}

func TestMoveRefusedLeavesSourceIntact(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	src := write(t, dir, "src", "keep me")
	dst := write(t, dir, "dst", "old")
	requireKind(t, exec(t, caseOf(f, "m", input("move", src, dst, nil))), pluginsdk.KindValidation, "destination exists")
	if read(t, src) != "keep me" {
		t.Fatal("the source of a refused move was changed")
	}
}

func TestSourceProblems(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	requireKind(t, exec(t, caseOf(f, "c", input("copy", filepath.Join(dir, "missing"), filepath.Join(dir, "d"), nil))), pluginsdk.KindValidation, "source not found")
	requireKind(t, exec(t, caseOf(f, "c", input("copy", dir, filepath.Join(dir, "d"), nil))), pluginsdk.KindValidation, "is a directory")
}

func TestMissingParentWithoutMkdirs(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	src := write(t, dir, "src", "x")
	o := exec(t, caseOf(f, "c", input("copy", src, filepath.Join(dir, "no", "such", "dst"), nil)))
	if o.Result.Error == nil {
		t.Fatal("expected a failure")
	}
	if _, err := os.Stat(filepath.Join(dir, "no")); err == nil {
		t.Fatal("directories were created without mkdirs")
	}
}

func TestDestinationIsDirectoryFails(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	src := write(t, dir, "src", "x")
	requireKind(t, exec(t, caseOf(f, "c", input("copy", src, dir, map[string]any{"overwrite": true}))), pluginsdk.KindValidation, "is a directory")
}

func TestInputValidation(t *testing.T) {
	good := input("copy", "/a", "/b", nil)
	for name, tc := range map[string]struct{ in, want string }{
		"good":               {good, ""},
		"bad mode":           {input("sync", "/a", "/b", nil), "mode"},
		"bad verify":         {input("copy", "/a", "/b", map[string]any{"verify": "md5"}), "verify"},
		"relative source":    {input("copy", "a", "/b", nil), "absolute"},
		"relative dest":      {input("copy", "/a", "b", nil), "absolute"},
		"dir source":         {input("copy", "/a/", "/b", nil), "single file"},
		"unknown field":      {input("copy", "/a", "/b", map[string]any{"glob": "*"}), "invalid input"},
		"unsupported scheme": {strings.Replace(good, `"sftp"`, `"ftp"`, 1), "not supported"},
		"no source":          {`{"mode":"copy","destination":{"node":{"name":"n","protocol":"sftp","path":"/b"}}}`, "source needs a node"},
		"newline in path":    {input("copy", "/a\nb", "/b", nil), "single-line"},
	} {
		t.Run(name, func(t *testing.T) {
			err := plug().Validate(context.Background(), json.RawMessage(tc.in))
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("Validate = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestNodeProblems(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	src := write(t, dir, "src", "x")
	in := input("copy", src, filepath.Join(dir, "d"), nil)

	c := caseOf(f, "no nodes", in)
	c.Nodes = nil
	requireKind(t, exec(t, c), pluginsdk.KindValidation, "not part of this step")

	c = caseOf(f, "winrm node", in)
	n := f.Node
	n.Transport = "winrm"
	c.Nodes = map[string]pluginsdk.NodeInfo{sshtest.NodeName: n}
	requireKind(t, exec(t, c), pluginsdk.KindValidation, "needs an ssh node")

	c = caseOf(f, "no credential", in)
	c.Bundle = nil
	if o := exec(t, c); o.Result.Error == nil || o.Result.Error.Kind != pluginsdk.KindAuth {
		t.Fatalf("step error = %v; want auth", o.Result.Error)
	}
}

func TestTofuKeyReported(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	src := write(t, dir, "src", "x")
	c := caseOf(f, "tofu", input("copy", src, filepath.Join(dir, "d"), nil))
	n := f.Node
	n.HostKey, n.HostKeyPolicy = "", "tofu"
	c.Nodes = map[string]pluginsdk.NodeInfo{sshtest.NodeName: n}
	o := exec(t, c)
	requireOK(t, o)
	keys := o.HostKeys.Keys()
	if len(keys) == 0 || keys[0].Node != sshtest.NodeName || keys[0].Key != f.Server.HostKeyLine() {
		t.Fatalf("reported = %+v; want the server key for %s", keys, sshtest.NodeName)
	}
}

func TestStrictHostKeyMismatchRefused(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	other := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	src := write(t, dir, "src", "x")
	c := caseOf(f, "bad key", input("copy", src, filepath.Join(dir, "d"), nil))
	n := f.Node
	n.HostKey = other.Server.HostKeyLine()
	c.Nodes = map[string]pluginsdk.NodeInfo{sshtest.NodeName: n}
	requireKind(t, exec(t, c), pluginsdk.KindConnection, "host key mismatch")
}

func TestCancelStopsAndCleansUp(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	fifo := "/dev/zero"
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	o := plugintest.Exec(ctx, t, plug(), caseOf(f, "c", input("copy", fifo, filepath.Join(dir, "dst"), nil)))
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancellation was not honored")
	}
	requireKind(t, o, pluginsdk.KindCancelled, "")
	if _, err := os.Stat(filepath.Join(dir, "dst")); err == nil {
		t.Fatal("a destination appeared after cancel")
	}
	if p := partials(t, dir); len(p) != 0 {
		t.Fatalf("partial files left after cancel: %v", p)
	}
}

func TestSecretsNeverLogged(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	src := write(t, dir, "src", "x")
	o := exec(t, caseOf(f, "c", input("copy", src, filepath.Join(dir, "d"), nil)))
	requireOK(t, o)
	all := o.LogOutput
	for _, l := range o.Emitter.Lines() {
		all += l.Line + "\n"
	}
	if strings.Contains(all, f.PrivateKey) || strings.Contains(all, fmt.Sprint(f.Bundle[sshtest.CredentialRef]["private_key"])) {
		t.Fatal("the private key reached the logs")
	}
}
