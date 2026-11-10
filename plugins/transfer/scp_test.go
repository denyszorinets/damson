//go:build unix

package transfer_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
	"github.com/denyszorinets/damson/plugins/internal/sshtest"
)

// requireSCP skips when this machine has no scp binary (the test server runs
// the real one). CI images have it.
func requireSCP(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("scp"); err != nil {
		t.Skip("scp is not installed")
	}
}

// Every protocol pair copies the same bytes: scp<->scp, scp<->sftp.
func TestSCPPairs(t *testing.T) {
	requireSCP(t)
	f := sshtest.NewFixture(t, "")
	content := strings.Repeat("scp payload ", 30000)
	for _, pair := range [][2]string{{"scp", "scp"}, {"scp", "sftp"}, {"sftp", "scp"}} {
		for _, level := range []string{"none", "size", "sha256"} {
			t.Run(pair[0]+"->"+pair[1]+"/"+level, func(t *testing.T) {
				dir := t.TempDir()
				src := write(t, dir, "src", content)
				dst := filepath.Join(dir, "dst")
				in := inputWith(pair[0], pair[1], "copy", src, dst, map[string]any{"verify": level})
				out := requireOK(t, exec_(t, caseOf(f, "c", in)))
				if out.Bytes != int64(len(content)) || out.SHA256 != sum(content) || read(t, dst) != content {
					t.Fatalf("output = %+v", out)
				}
				if p := partials(t, dir); len(p) != 0 {
					t.Fatalf("partial files left: %v", p)
				}
			})
		}
	}
}

func TestSCPAwkwardNames(t *testing.T) {
	requireSCP(t)
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	for _, name := range []string{"with space.txt", "it's.txt", `dollar$HOME "q".txt`, "semi;colon&.txt", "ünï.txt"} {
		src := write(t, dir, name, "x-"+name)
		dst := filepath.Join(dir, "out", name)
		in := inputWith("scp", "scp", "copy", src, dst, map[string]any{"mkdirs": true, "verify": "sha256"})
		requireOK(t, exec_(t, caseOf(f, name, in)))
		if read(t, dst) != "x-"+name {
			t.Fatalf("%q copied wrong", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "never")); err == nil {
		t.Fatal("unexpected file")
	}
}

func TestSCPEmptyFile(t *testing.T) {
	requireSCP(t)
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	src := write(t, dir, "empty", "")
	out := requireOK(t, exec_(t, caseOf(f, "e", inputWith("scp", "scp", "copy", src, filepath.Join(dir, "d"), map[string]any{"verify": "sha256"}))))
	if out.Bytes != 0 || out.SHA256 != sum("") {
		t.Fatalf("output = %+v", out)
	}
}

func TestSCPOverwriteMoveAndDirectoryDestination(t *testing.T) {
	requireSCP(t)
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	src := write(t, dir, "src", "new")
	dst := write(t, dir, "dst", "old")
	requireKind(t, exec_(t, caseOf(f, "c", inputWith("scp", "scp", "copy", src, dst, nil))), pluginsdk.KindValidation, "destination exists")
	if read(t, dst) != "old" {
		t.Fatal("a refused transfer changed the destination")
	}
	requireOK(t, exec_(t, caseOf(f, "c", inputWith("scp", "scp", "copy", src, dst, map[string]any{"overwrite": true}))))
	if read(t, dst) != "new" {
		t.Fatal("overwrite did not replace the file")
	}
	requireOK(t, exec_(t, caseOf(f, "m", inputWith("scp", "scp", "move", src, dir+"/moved/", map[string]any{"mkdirs": true}))))
	if read(t, dir+"/moved/src") != "new" {
		t.Fatal("move to a directory failed")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
}

func TestSCPSourceProblems(t *testing.T) {
	requireSCP(t)
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	requireKind(t, exec_(t, caseOf(f, "c", inputWith("scp", "scp", "copy", filepath.Join(dir, "missing"), filepath.Join(dir, "d"), nil))), pluginsdk.KindValidation, "source not found")
	requireKind(t, exec_(t, caseOf(f, "c", inputWith("scp", "scp", "copy", dir, filepath.Join(dir, "d"), nil))), pluginsdk.KindValidation, "is a directory")
}

func TestSCPWindowsNodeRefused(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	src := write(t, dir, "src", "x")
	c := caseOf(f, "w", inputWith("scp", "scp", "copy", src, filepath.Join(dir, "d"), nil))
	n := f.Node
	n.Transport = "winrm"
	c.Nodes = map[string]pluginsdk.NodeInfo{sshtest.NodeName: n}
	requireKind(t, exec_(t, c), pluginsdk.KindValidation, "needs an ssh node")
}

// Canceling a running scp transfer returns promptly and leaves no partial file.
func TestSCPCancel(t *testing.T) {
	requireSCP(t)
	f := sshtest.NewFixture(t, "")
	dir := t.TempDir()
	// A sparse 8 GiB file: the copy runs far longer than the cancel delay.
	src := filepath.Join(dir, "big")
	fh, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := fh.Truncate(8 << 30); err != nil {
		t.Skipf("cannot create a sparse file here: %v", err)
	}
	_ = fh.Close()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	o := plugintest.Exec(ctx, t, plug(), caseOf(f, "c", inputWith("scp", "scp", "copy", src, filepath.Join(dir, "dst"), nil)))
	if time.Since(start) > 15*time.Second {
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
