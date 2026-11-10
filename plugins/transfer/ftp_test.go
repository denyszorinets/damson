//go:build unix

package transfer_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
	"github.com/denyszorinets/damson/plugins/internal/ftptest"
	"github.com/denyszorinets/damson/plugins/internal/sshtest"
	"github.com/denyszorinets/damson/plugins/transfer"
)

const (
	ftpUser = "ftpuser"
	ftpPass = "s3cret-ftp-PASSWORD"
	ftpRef  = "secret://ftp/cred"
)

// ftpNode makes a node for the server, named name.
func ftpNode(name string, s *ftptest.Server, attrs map[string]any) pluginsdk.NodeInfo {
	a := map[string]any{"ftp_port": float64(s.Port())}
	for k, v := range attrs {
		a[k] = v
	}
	return pluginsdk.NodeInfo{Name: name, Address: s.Host(), Transport: "ssh", OS: "linux", Credential: ftpRef, Attributes: a}
}

type side struct{ node, proto, path string }

// twoSide builds a transfer input between two arbitrary endpoints.
func twoSide(mode string, src, dst side, extra map[string]any) string {
	ep := func(s side) map[string]any {
		return map[string]any{"node": map[string]any{"name": s.node, "protocol": s.proto, "path": s.path}}
	}
	m := map[string]any{"mode": mode, "source": ep(src), "destination": ep(dst)}
	for k, v := range extra {
		m[k] = v
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

func ftpCase(name, in string, cfg map[string]string, nodes ...pluginsdk.NodeInfo) plugintest.Case {
	c := plugintest.Case{
		Name: name, Input: json.RawMessage(in), Config: cfg,
		Nodes:  map[string]pluginsdk.NodeInfo{},
		Bundle: map[string]map[string]string{ftpRef: {"username": ftpUser, "password": ftpPass}},
	}
	for _, n := range nodes {
		c.Nodes[n.Name] = n
	}
	return c
}

func ftpServer(t *testing.T, mode ftptest.Mode) (*ftptest.Server, map[string]string, map[string]any) {
	s := ftptest.Start(t, ftptest.Options{User: ftpUser, Password: ftpPass, Mode: mode})
	cfg := map[string]string{transfer.ConfigAllowPlainFTP: "true"}
	if s.CAPEM != "" {
		cfg[transfer.ConfigTLSCAPEM] = s.CAPEM
	}
	attrs := map[string]any{}
	if mode == ftptest.Implicit {
		attrs["ftps_mode"] = "implicit"
	}
	return s, cfg, attrs
}

// ftp, ftps (explicit) and ftps (implicit) copy the same bytes at every verify level.
func TestFTPFamilyCopies(t *testing.T) {
	content := strings.Repeat("ftp payload ", 30000)
	for name, tc := range map[string]struct {
		mode  ftptest.Mode
		proto string
	}{
		"ftp":           {ftptest.Plain, "ftp"},
		"ftps explicit": {ftptest.Explicit, "ftps"},
		"ftps implicit": {ftptest.Implicit, "ftps"},
	} {
		t.Run(name, func(t *testing.T) {
			s, cfg, attrs := ftpServer(t, tc.mode)
			n := ftpNode("ftpnode", s, attrs)
			for _, level := range []string{"none", "size", "sha256"} {
				dir := t.TempDir()
				src := write(t, dir, "src", content)
				dst := filepath.Join(dir, "dst")
				in := twoSide("copy", side{"ftpnode", tc.proto, src}, side{"ftpnode", tc.proto, dst}, map[string]any{"verify": level})
				out := requireOK(t, exec_(t, ftpCase("c", in, cfg, n)))
				if out.Bytes != int64(len(content)) || out.SHA256 != sum(content) || read(t, dst) != content {
					t.Fatalf("%s: output = %+v", level, out)
				}
				if p := partials(t, dir); len(p) != 0 {
					t.Fatalf("partial files left: %v", p)
				}
			}
		})
	}
}

func TestFTPEmptyFileAndMkdirsAndDirectoryDestination(t *testing.T) {
	s, cfg, _ := ftpServer(t, ftptest.Plain)
	n := ftpNode("ftpnode", s, nil)
	dir := t.TempDir()
	empty := write(t, dir, "empty", "")
	out := requireOK(t, exec_(t, ftpCase("e", twoSide("copy", side{"ftpnode", "ftp", empty}, side{"ftpnode", "ftp", dir + "/a/b/e"}, map[string]any{"mkdirs": true, "verify": "sha256"}), cfg, n)))
	if out.Bytes != 0 || out.SHA256 != sum("") {
		t.Fatalf("output = %+v", out)
	}
	src := write(t, dir, "report.txt", "x")
	out = requireOK(t, exec_(t, ftpCase("d", twoSide("copy", side{"ftpnode", "ftp", src}, side{"ftpnode", "ftp", dir + "/out/"}, map[string]any{"mkdirs": true}), cfg, n)))
	if out.DestinationPath != dir+"/out/report.txt" || read(t, out.DestinationPath) != "x" {
		t.Fatalf("output = %+v", out)
	}
}

func TestFTPOverwriteAndMove(t *testing.T) {
	s, cfg, _ := ftpServer(t, ftptest.Plain)
	n := ftpNode("ftpnode", s, nil)
	dir := t.TempDir()
	src := write(t, dir, "src", "new")
	dst := write(t, dir, "dst", "old")
	tr := func(mode string, extra map[string]any) plugintest.Outcome {
		return exec_(t, ftpCase("c", twoSide(mode, side{"ftpnode", "ftp", src}, side{"ftpnode", "ftp", dst}, extra), cfg, n))
	}
	requireKind(t, tr("copy", nil), pluginsdk.KindValidation, "destination exists")
	requireKind(t, tr("move", nil), pluginsdk.KindValidation, "destination exists")
	if read(t, dst) != "old" || read(t, src) != "new" || len(partials(t, dir)) != 0 {
		t.Fatal("a refused transfer changed something or left a partial")
	}
	requireOK(t, tr("move", map[string]any{"overwrite": true}))
	if read(t, dst) != "new" {
		t.Fatal("overwrite did not replace the file")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source of the move still exists: %v", err)
	}
}

func TestFTPSourceProblems(t *testing.T) {
	s, cfg, _ := ftpServer(t, ftptest.Plain)
	n := ftpNode("ftpnode", s, nil)
	dir := t.TempDir()
	run := func(srcPath string) plugintest.Outcome {
		return exec_(t, ftpCase("c", twoSide("copy", side{"ftpnode", "ftp", srcPath}, side{"ftpnode", "ftp", filepath.Join(dir, "d")}, nil), cfg, n))
	}
	requireKind(t, run(filepath.Join(dir, "missing")), pluginsdk.KindValidation, "source not found")
	requireKind(t, run(dir), pluginsdk.KindValidation, "is a directory")
}

// ftp and sftp interoperate across two different nodes.
func TestFTPAndSFTPAcrossNodes(t *testing.T) {
	s, cfg, _ := ftpServer(t, ftptest.Plain)
	f := sshtest.NewFixture(t, "")
	ftpN := ftpNode("ftpnode", s, nil)
	sshN := f.Node
	sshN.Name = "sshnode"
	dir := t.TempDir()
	content := strings.Repeat("cross ", 5000)
	src := write(t, dir, "src", content)
	mk := func(name, in string) plugintest.Case {
		c := ftpCase(name, in, cfg, ftpN, sshN)
		c.Bundle[sshtest.CredentialRef] = f.Bundle[sshtest.CredentialRef]
		return c
	}
	sshN.Credential = sshtest.CredentialRef
	requireOK(t, exec_(t, mk("a", twoSide("copy", side{"ftpnode", "ftp", src}, side{"sshnode", "sftp", dir + "/via-sftp"}, map[string]any{"verify": "sha256"}))))
	requireOK(t, exec_(t, mk("b", twoSide("copy", side{"sshnode", "scp", dir + "/via-sftp"}, side{"ftpnode", "ftp", dir + "/via-ftp"}, map[string]any{"verify": "sha256"}))))
	if read(t, dir+"/via-sftp") != content || read(t, dir+"/via-ftp") != content {
		t.Fatal("content differs after the round trip")
	}
}

func TestFTPAuthFailureDoesNotLeak(t *testing.T) {
	s, cfg, _ := ftpServer(t, ftptest.Plain)
	n := ftpNode("ftpnode", s, nil)
	dir := t.TempDir()
	src := write(t, dir, "src", "x")
	c := ftpCase("c", twoSide("copy", side{"ftpnode", "ftp", src}, side{"ftpnode", "ftp", dir + "/d"}, nil), cfg, n)
	c.Bundle[ftpRef] = map[string]string{"username": ftpUser, "password": "wrong-PASSWORD-guess"}
	o := exec_(t, c)
	requireKind(t, o, pluginsdk.KindAuth, "logging in")
	all := o.LogOutput + o.Result.Error.Message
	for _, l := range o.Emitter.Lines() {
		all += l.Line
	}
	if strings.Contains(all, "wrong-PASSWORD-guess") || strings.Contains(all, ftpPass) {
		t.Fatal("a password reached the logs or the error")
	}
}

func TestFTPCredentialAndAttributeProblems(t *testing.T) {
	s, cfg, _ := ftpServer(t, ftptest.Plain)
	dir := t.TempDir()
	src := write(t, dir, "src", "x")
	in := twoSide("copy", side{"ftpnode", "ftp", src}, side{"ftpnode", "ftp", dir + "/d"}, nil)

	noCred := ftpNode("ftpnode", s, nil)
	noCred.Credential = ""
	requireKind(t, exec_(t, ftpCase("c", in, cfg, noCred)), pluginsdk.KindAuth, "has no credential")

	c := ftpCase("c", in, cfg, ftpNode("ftpnode", s, nil))
	c.Bundle[ftpRef] = map[string]string{"username": ftpUser}
	requireKind(t, exec_(t, c), pluginsdk.KindAuth, "username and a password")

	requireKind(t, exec_(t, ftpCase("c", in, cfg, ftpNode("ftpnode", s, map[string]any{"ftp_port": "21"}))), pluginsdk.KindValidation, "ftp_port")
	requireKind(t, exec_(t, ftpCase("c", in, cfg, ftpNode("ftpnode", s, map[string]any{"ftp_port": float64(70000)}))), pluginsdk.KindValidation, "ftp_port")

	ftpsIn := strings.ReplaceAll(in, `"ftp"`, `"ftps"`)
	requireKind(t, exec_(t, ftpCase("c", ftpsIn, nil, ftpNode("ftpnode", s, map[string]any{"ftps_mode": "bogus"}))), pluginsdk.KindValidation, "ftps_mode")
}

// ftps verifies the server certificate: an unknown CA is refused, and the
// operator's CA makes it work. A bad tls_ca_pem is refused.
func TestFTPSVerifiesCertificates(t *testing.T) {
	s, cfg, _ := ftpServer(t, ftptest.Explicit)
	n := ftpNode("ftpnode", s, nil)
	dir := t.TempDir()
	src := write(t, dir, "src", "x")
	in := twoSide("copy", side{"ftpnode", "ftps", src}, side{"ftpnode", "ftps", dir + "/d"}, nil)

	o := exec_(t, ftpCase("c", in, nil, n))
	if o.Result.Error == nil || o.Result.Error.Kind != pluginsdk.KindConnection || !strings.Contains(o.Result.Error.Message, "certificate") {
		t.Fatalf("without the CA: %v", o.Result.Error)
	}
	requireKind(t, exec_(t, ftpCase("c", in, map[string]string{transfer.ConfigTLSCAPEM: "not pem"}, n)), pluginsdk.KindValidation, transfer.ConfigTLSCAPEM)
	requireOK(t, exec_(t, ftpCase("c", in, cfg, n)))

	// Plain ftp against an ftps-only server never sends credentials in clear.
	plainIn := strings.ReplaceAll(in, `"ftps"`, `"ftp"`)
	if o := exec_(t, ftpCase("c", plainIn, nil, n)); o.Result.Error == nil {
		t.Fatal("plain ftp worked against a server that requires TLS")
	}
}

func TestFTPCancel(t *testing.T) {
	s, _, _ := ftpServer(t, ftptest.Plain)
	n := ftpNode("ftpnode", s, nil)
	dir := t.TempDir()
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
	time.AfterFunc(400*time.Millisecond, cancel)
	start := time.Now()
	o := plugintest.Exec(ctx, t, plug(), ftpCase("c", twoSide("copy", side{"ftpnode", "ftp", src}, side{"ftpnode", "ftp", dir + "/dst"}, nil), map[string]string{transfer.ConfigAllowPlainFTP: "true"}, n))
	if time.Since(start) > 15*time.Second {
		t.Fatal("cancellation was not honored")
	}
	requireKind(t, o, pluginsdk.KindCancelled, "")
	if _, err := os.Stat(filepath.Join(dir, "dst")); err == nil {
		t.Fatal("a destination appeared after cancel")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(partials(t, dir)) != 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if p := partials(t, dir); len(p) != 0 {
		t.Fatalf("partial files left after cancel: %v", p)
	}
}

// ftps never falls back to clear text: a server without AUTH TLS stops the
// client before it sends the username or password.
func TestFTPSRefusesServerWithoutTLS(t *testing.T) {
	s, _, _ := ftpServer(t, ftptest.Plain)
	n := ftpNode("ftpnode", s, nil)
	dir := t.TempDir()
	src := write(t, dir, "src", "x")
	in := twoSide("copy", side{"ftpnode", "ftps", src}, side{"ftpnode", "ftps", dir + "/d"}, nil)
	o := exec_(t, ftpCase("c", in, nil, n))
	if o.Result.Error == nil || o.Result.Error.Kind != pluginsdk.KindConnection {
		t.Fatalf("step error = %v; want a connection error", o.Result.Error)
	}
	if s.AuthAttempts() != 0 {
		t.Fatalf("credentials reached a server without TLS (%d logins)", s.AuthAttempts())
	}
}

// A hostname node is verified against the certificate's DNS names, and an IP
// node is not accepted by a certificate that has only a DNS name.
func TestFTPSVerifiesHostnames(t *testing.T) {
	s := ftptest.Start(t, ftptest.Options{User: ftpUser, Password: ftpPass, Mode: ftptest.Explicit, DNSOnly: true})
	cfg := map[string]string{transfer.ConfigTLSCAPEM: s.CAPEM}
	dir := t.TempDir()
	src := write(t, dir, "src", "x")
	in := twoSide("copy", side{"ftpnode", "ftps", src}, side{"ftpnode", "ftps", dir + "/d"}, map[string]any{"overwrite": true})

	byName := ftpNode("ftpnode", s, nil)
	byName.Address = "localhost"
	requireOK(t, exec_(t, ftpCase("c", in, cfg, byName)))

	byIP := ftpNode("ftpnode", s, nil)
	o := exec_(t, ftpCase("c", in, cfg, byIP))
	if o.Result.Error == nil || !strings.Contains(o.Result.Error.Message, "certificate") {
		t.Fatalf("an IP node was accepted by a DNS-only certificate: %v", o.Result.Error)
	}
}

// A rename that fails for another reason must not cost the old destination.
func TestFTPOverwriteKeepsOldFileWhenRenameFails(t *testing.T) {
	s, _, _ := ftpServer(t, ftptest.Plain)
	n := ftpNode("ftpnode", s, nil)
	dir := t.TempDir()
	src := write(t, dir, "src", "new")
	dst := write(t, dir, "dst", "old")
	// A destination directory that cannot be written makes the rename fail.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	o := exec_(t, ftpCase("c", twoSide("copy", side{"ftpnode", "ftp", src}, side{"ftpnode", "ftp", dst}, map[string]any{"overwrite": true}), nil, n))
	if o.Result.Error == nil {
		t.Fatal("expected a failure")
	}
	if read(t, dst) != "old" {
		t.Fatal("the old destination was lost")
	}
}

func TestPlainFTPNeedsTheAgentOptIn(t *testing.T) {
	s, cfg, attrs := ftpServer(t, ftptest.Plain)
	n := ftpNode("ftpnode", s, attrs)
	dir := t.TempDir()
	src := write(t, dir, "src", "data")
	in := twoSide("copy", side{"ftpnode", "ftp", src}, side{"ftpnode", "ftp", filepath.Join(dir, "dst")}, nil)
	requireKind(t, exec_(t, ftpCase("c", in, nil, n)), pluginsdk.KindValidation, "agent.allow_plain_ftp")
	requireKind(t, exec_(t, ftpCase("c", in, map[string]string{transfer.ConfigAllowPlainFTP: "false"}, n)), pluginsdk.KindValidation, "agent.allow_plain_ftp")
	requireOK(t, exec_(t, ftpCase("c", in, cfg, n)))
}
