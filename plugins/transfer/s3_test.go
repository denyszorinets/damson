//go:build unix

package transfer_test

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
	"github.com/denyszorinets/damson/plugins/internal/s3test"
	"github.com/denyszorinets/damson/plugins/internal/sshtest"
	"github.com/denyszorinets/damson/plugins/transfer"
)

const s3Ref = "secret://s3/conn"

// s3Side is an S3 endpoint for twoSide-like inputs.
type s3Side struct{ bucket, key string }

// endpointJSON renders a node or an s3 endpoint.
func endpointJSON(e any) map[string]any {
	switch v := e.(type) {
	case side:
		return map[string]any{"node": map[string]any{"name": v.node, "protocol": v.proto, "path": v.path}}
	case s3Side:
		return map[string]any{"s3": map[string]any{"connection": s3Ref, "bucket": v.bucket, "key": v.key}}
	}
	panic("unknown endpoint")
}

func mixed(mode string, src, dst any, extra map[string]any) string {
	m := map[string]any{"mode": mode, "source": endpointJSON(src), "destination": endpointJSON(dst)}
	for k, v := range extra {
		m[k] = v
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

// s3Case runs in against the fixture's ssh node and the fake S3 server.
func s3Case(f *sshtest.Fixture, srv *s3test.Server, in string) plugintest.Case {
	c := caseOf(f, "s3", in)
	c.Bundle = map[string]map[string]string{sshtest.CredentialRef: f.Bundle[sshtest.CredentialRef]}
	if srv != nil {
		c.Bundle[s3Ref] = srv.Connection()
	}
	return c
}

// getObject reads an object back through a plain transfer to a node file.
func getObject(t *testing.T, f *sshtest.Fixture, srv *s3test.Server, bucket, key string) string {
	t.Helper()
	dir := t.TempDir()
	dst := filepath.Join(dir, "got")
	requireOK(t, exec_(t, s3Case(f, srv, mixed("copy", s3Side{bucket, key}, side{sshtest.NodeName, "sftp", dst}, nil))))
	b, _ := io.ReadAll(strings.NewReader(read(t, dst)))
	return string(b)
}

func TestS3RoundTripAtEveryVerifyLevel(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	srv := s3test.Start(t, "bucket-one")
	content := strings.Repeat("s3 payload ", 20000)
	for _, level := range []string{"none", "size", "sha256"} {
		t.Run(level, func(t *testing.T) {
			dir := t.TempDir()
			src := write(t, dir, "src", content)
			out := requireOK(t, exec_(t, s3Case(f, srv, mixed("copy", side{sshtest.NodeName, "sftp", src}, s3Side{"bucket-one", "dir/obj-" + level}, map[string]any{"verify": level}))))
			if out.DestinationKey != "dir/obj-"+level || out.DestinationPath != "" || out.Bytes != int64(len(content)) || out.SHA256 != sum(content) || out.ETag == "" {
				t.Fatalf("output = %+v", out)
			}
			if got := getObject(t, f, srv, "bucket-one", "dir/obj-"+level); got != content {
				t.Fatal("the object differs from the source")
			}
		})
	}
}

func TestS3ToS3AndKeyPrefixDestination(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	srv := s3test.Start(t, "bucket-one", "bucket-two")
	dir := t.TempDir()
	src := write(t, dir, "report.csv", "a,b,c")
	requireOK(t, exec_(t, s3Case(f, srv, mixed("copy", side{sshtest.NodeName, "sftp", src}, s3Side{"bucket-one", "in/report.csv"}, nil))))
	// s3 -> s3 needs no node at all, and a key ending in / gets the name appended.
	c := s3Case(f, srv, mixed("copy", s3Side{"bucket-one", "in/report.csv"}, s3Side{"bucket-two", "archive/2026/"}, map[string]any{"verify": "sha256"}))
	c.Nodes = nil
	out := requireOK(t, exec_(t, c))
	if out.DestinationKey != "archive/2026/report.csv" {
		t.Fatalf("output = %+v", out)
	}
	if got := getObject(t, f, srv, "bucket-two", "archive/2026/report.csv"); got != "a,b,c" {
		t.Fatalf("object = %q", got)
	}
}

func TestS3OverwriteAndMove(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	srv := s3test.Start(t, "bucket-one")
	dir := t.TempDir()
	src := write(t, dir, "src", "new")
	put := func(mode string, extra map[string]any) plugintest.Outcome {
		return exec_(t, s3Case(f, srv, mixed(mode, side{sshtest.NodeName, "sftp", src}, s3Side{"bucket-one", "k"}, extra)))
	}
	requireOK(t, put("copy", nil))
	requireKind(t, put("copy", nil), pluginsdk.KindValidation, "destination exists")
	requireKind(t, put("move", nil), pluginsdk.KindValidation, "destination exists")
	if read(t, src) != "new" {
		t.Fatal("the source of a refused move was changed")
	}
	requireOK(t, put("move", map[string]any{"overwrite": true}))
	if got := getObject(t, f, srv, "bucket-one", "k"); got != "new" {
		t.Fatalf("object = %q", got)
	}
	if _, ok := statLocal(src); ok {
		t.Fatal("the source of the move still exists")
	}
}

// Moving out of S3 deletes the object only after the copy is verified.
func TestS3MoveFromS3(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	srv := s3test.Start(t, "bucket-one")
	dir := t.TempDir()
	src := write(t, dir, "src", "payload")
	requireOK(t, exec_(t, s3Case(f, srv, mixed("copy", side{sshtest.NodeName, "sftp", src}, s3Side{"bucket-one", "obj"}, nil))))
	dst := filepath.Join(dir, "dst")
	requireOK(t, exec_(t, s3Case(f, srv, mixed("move", s3Side{"bucket-one", "obj"}, side{sshtest.NodeName, "sftp", dst}, map[string]any{"verify": "sha256"}))))
	if read(t, dst) != "payload" {
		t.Fatal("content wrong")
	}
	requireKind(t, exec_(t, s3Case(f, srv, mixed("copy", s3Side{"bucket-one", "obj"}, side{sshtest.NodeName, "sftp", dst + "2"}, nil))), pluginsdk.KindValidation, "source not found")
}

func TestS3Problems(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	srv := s3test.Start(t, "bucket-one")
	dir := t.TempDir()
	src := write(t, dir, "src", "x")
	to := func(b, k string) string { return mixed("copy", side{sshtest.NodeName, "sftp", src}, s3Side{b, k}, nil) }

	requireKind(t, exec_(t, s3Case(f, srv, to("nobucket", "k"))), pluginsdk.KindValidation, "bucket")

	c := s3Case(f, srv, to("bucket-one", "k"))
	c.Bundle[s3Ref] = map[string]string{"endpoint": srv.URL(), "region": "us-east-1"}
	requireKind(t, exec_(t, c), pluginsdk.KindAuth, "access_key")

	c = s3Case(f, srv, to("bucket-one", "k"))
	c.Bundle[s3Ref]["endpoint"] = "ftp://example.com"
	requireKind(t, exec_(t, c), pluginsdk.KindValidation, "endpoint")
	for _, bad := range []string{"https://user:hunter2pw@example.com", "https://example.com?x=1", "https://example.com#f"} {
		c.Bundle[s3Ref]["endpoint"] = bad
		o := exec_(t, c)
		requireKind(t, o, pluginsdk.KindValidation, "endpoint")
		if strings.Contains(o.Result.Error.Message, "hunter2pw") {
			t.Fatal("the endpoint password reached the error")
		}
	}

	c = s3Case(f, srv, to("bucket-one", "k"))
	c.Bundle[s3Ref]["path_style"] = "maybe"
	requireKind(t, exec_(t, c), pluginsdk.KindValidation, "path_style")

	c = s3Case(f, srv, to("bucket-one", "k"))
	delete(c.Bundle, s3Ref)
	requireKind(t, exec_(t, c), pluginsdk.KindAuth, "s3 connection")
}

// S3 credentials never reach a log or an output, also on failure.
func TestS3SecretsNeverLeak(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	srv := s3test.Start(t, "bucket-one")
	dir := t.TempDir()
	src := write(t, dir, "src", "x")
	for name, in := range map[string]string{
		"ok":          mixed("copy", side{sshtest.NodeName, "sftp", src}, s3Side{"bucket-one", "k"}, nil),
		"nobucket":    mixed("copy", side{sshtest.NodeName, "sftp", src}, s3Side{"missing", "k"}, nil),
		"nosrcobj":    mixed("copy", s3Side{"bucket-one", "absent"}, side{sshtest.NodeName, "sftp", dir + "/d"}, nil),
		"badendpoint": mixed("copy", side{sshtest.NodeName, "sftp", src}, s3Side{"bucket-one", "k2"}, nil),
	} {
		c := s3Case(f, srv, in)
		if name == "badendpoint" {
			c.Bundle[s3Ref]["endpoint"] = "http://127.0.0.1:1"
		}
		o := exec_(t, c)
		all := o.LogOutput + string(o.Result.Outputs)
		if o.Result.Error != nil {
			all += o.Result.Error.Message
		}
		for _, l := range o.Emitter.Lines() {
			all += l.Line + "\n"
		}
		if strings.Contains(all, s3test.SecretKey) || strings.Contains(all, s3test.AccessKey) {
			t.Fatalf("%s: an S3 credential reached the output: %s", name, all)
		}
	}
}

func TestS3InputValidation(t *testing.T) {
	ep := func(s string) string { return s }
	node := `{"node":{"name":"n","protocol":"sftp","path":"/a"}}`
	good := `{"s3":{"connection":"secret://s3/conn","bucket":"bkt","key":"k"}}`
	for name, tc := range map[string]struct{ src, dst, want string }{
		"ok":             {node, good, ""},
		"both":           {node, `{"node":{"name":"n","protocol":"sftp","path":"/a"},"s3":{"connection":"secret://s3/conn","bucket":"bkt","key":"k"}}`, "not both"},
		"literal secret": {node, `{"s3":{"connection":"hunter2","bucket":"bkt","key":"k"}}`, "secret:// ref"},
		"empty bucket":   {node, `{"s3":{"connection":"secret://s3/conn","bucket":"","key":"k"}}`, "bucket"},
		"slash bucket":   {node, `{"s3":{"connection":"secret://s3/conn","bucket":"a/b","key":"k"}}`, "bucket"},
		"leading slash":  {node, `{"s3":{"connection":"secret://s3/conn","bucket":"bkt","key":"/k"}}`, "key"},
		"prefix source":  {`{"s3":{"connection":"secret://s3/conn","bucket":"bkt","key":"dir/"}}`, node, "single file"},
		"neither":        {node, `{}`, "needs a node or s3"},
	} {
		t.Run(name, func(t *testing.T) {
			in := `{"mode":"copy","source":` + ep(tc.src) + `,"destination":` + ep(tc.dst) + `}`
			err := plug().Validate(t.Context(), json.RawMessage(in))
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("Validate = %v; want %q", err, tc.want)
			}
		})
	}
}

func statLocal(p string) (os.FileInfo, bool) {
	fi, err := os.Stat(p)
	return fi, err == nil
}

// An https s3 endpoint is verified against the system roots plus the operator's
// CA; there is no way around it.
func TestS3VerifiesCertificates(t *testing.T) {
	f := sshtest.NewFixture(t, "")
	srv := s3test.StartTLS(t, "bucket-one")
	dir := t.TempDir()
	src := write(t, dir, "src", "tls payload")
	in := mixed("copy", side{sshtest.NodeName, "sftp", src}, s3Side{"bucket-one", "k"}, nil)

	o := exec_(t, s3Case(f, srv, in))
	if o.Result.Error == nil || !strings.Contains(o.Result.Error.Message, "certificate") {
		t.Fatalf("without the CA: %v", o.Result.Error)
	}

	c := s3Case(f, srv, in)
	c.Config = map[string]string{transfer.ConfigTLSCAPEM: "not pem"}
	requireKind(t, exec_(t, c), pluginsdk.KindValidation, transfer.ConfigTLSCAPEM)

	c = s3Case(f, srv, in)
	c.Config = map[string]string{transfer.ConfigTLSCAPEM: srv.CAPEM}
	if out := requireOK(t, exec_(t, c)); out.ETag == "" || out.DestinationKey != "k" {
		t.Fatalf("output = %+v", out)
	}
}
