//go:build integration && unix

package transfer_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/pkg/sftp"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	gossh "golang.org/x/crypto/ssh"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
	"github.com/denyszorinets/damson/plugins/internal/s3test"
	"github.com/denyszorinets/damson/plugins/internal/sshtest"
)

const (
	itUser = "damson"
	itPort = "2222/tcp"
)

// startOpenSSH starts a real sshd (the image the ssh plugin tests use) and
// returns a node for it, its private key and an sftp client for preparing and
// inspecting files.
func startOpenSSH(t *testing.T) (pluginsdk.NodeInfo, string, *sftp.Client) {
	t.Helper()
	priv, pub := sshtest.NewClientKey(t, "")
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "lscr.io/linuxserver/openssh-server:version-10.3_p1-r1",
			ExposedPorts: []string{itPort},
			Env: map[string]string{
				"PUID": "1000", "PGID": "1000", "TZ": "UTC",
				"USER_NAME": itUser, "PUBLIC_KEY": sshtest.KeyLine(pub),
				"PASSWORD_ACCESS": "false", "SUDO_ACCESS": "false",
			},
			WaitingFor: wait.ForListeningPort(itPort).WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("cannot start sshd container (is Docker available?): %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := ctr.MappedPort(ctx, itPort)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.ParsePrivateKey([]byte(priv))
	if err != nil {
		t.Fatal(err)
	}
	var client *sftp.Client
	deadline := time.Now().Add(60 * time.Second)
	for {
		sc, derr := gossh.Dial("tcp", host+":"+port.Port(), &gossh.ClientConfig{
			User: itUser, Auth: []gossh.AuthMethod{gossh.PublicKeys(signer)},
			HostKeyCallback: gossh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second, //nolint:gosec // test container
		})
		if derr == nil {
			t.Cleanup(func() { _ = sc.Close() })
			if client, derr = sftp.NewClient(sc); derr == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("sshd never accepted the key: %v", derr)
		}
		time.Sleep(time.Second)
	}
	t.Cleanup(func() { _ = client.Close() })
	node := pluginsdk.NodeInfo{
		Name: sshtest.NodeName, Address: host, Port: int(port.Num()), Transport: "ssh", OS: "linux",
		Credential: sshtest.CredentialRef, HostKeyPolicy: "tofu",
	}
	return node, priv, client
}

func putFile(t *testing.T, c *sftp.Client, p, content string) {
	t.Helper()
	f, err := c.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func getFile(t *testing.T, c *sftp.Client, p string) (string, bool) {
	t.Helper()
	f, err := c.Open(p)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

func TestIntegrationOpenSSH(t *testing.T) {
	node, priv, files := startOpenSSH(t)
	home, err := files.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	run := func(in string) plugintest.Outcome {
		return exec_(t, plugintest.Case{
			Name: "it", Input: json.RawMessage(in),
			Nodes:  map[string]pluginsdk.NodeInfo{sshtest.NodeName: node},
			Bundle: map[string]map[string]string{sshtest.CredentialRef: {"username": itUser, "private_key": priv}},
		})
	}
	content := strings.Repeat("0123456789", 100000)

	t.Run("copy with sha256 and mkdirs", func(t *testing.T) {
		putFile(t, files, home+"/a.bin", content)
		out := requireOK(t, run(input("copy", home+"/a.bin", home+"/new/dir/b.bin", map[string]any{"verify": "sha256", "mkdirs": true})))
		if got, _ := getFile(t, files, home+"/new/dir/b.bin"); got != content || out.SHA256 != sum(content) {
			t.Fatalf("copy differs (output %+v)", out)
		}
	})

	t.Run("overwrite is refused then allowed, with no partial left", func(t *testing.T) {
		putFile(t, files, home+"/o-src", "new")
		putFile(t, files, home+"/o-dst", "old")
		requireKind(t, run(input("copy", home+"/o-src", home+"/o-dst", nil)), pluginsdk.KindValidation, "destination exists")
		if got, _ := getFile(t, files, home+"/o-dst"); got != "old" {
			t.Fatalf("destination = %q", got)
		}
		requireOK(t, run(input("copy", home+"/o-src", home+"/o-dst", map[string]any{"overwrite": true})))
		if got, _ := getFile(t, files, home+"/o-dst"); got != "new" {
			t.Fatalf("destination = %q", got)
		}
		infos, _ := files.ReadDir(home)
		for _, i := range infos {
			if strings.Contains(i.Name(), ".damson-partial-") {
				t.Fatalf("partial file left: %s", i.Name())
			}
		}
	})

	t.Run("move removes the source", func(t *testing.T) {
		putFile(t, files, home+"/m-src", "moving")
		requireOK(t, run(input("move", home+"/m-src", home+"/m-dst/", map[string]any{"mkdirs": true})))
		if got, _ := getFile(t, files, home+"/m-dst/m-src"); got != "moving" {
			t.Fatalf("destination = %q", got)
		}
		if _, ok := getFile(t, files, home+"/m-src"); ok {
			t.Fatal("the source of a move still exists")
		}
	})

	t.Run("scp and sftp interoperate", func(t *testing.T) {
		putFile(t, files, home+"/s-src", content)
		requireOK(t, run(inputWith("scp", "sftp", "copy", home+"/s-src", home+"/s-dst", map[string]any{"verify": "sha256"})))
		if got, _ := getFile(t, files, home+"/s-dst"); got != content {
			t.Fatal("scp to sftp copy differs")
		}
		requireOK(t, run(inputWith("sftp", "scp", "move", home+"/s-dst", home+"/s-moved", map[string]any{"verify": "sha256"})))
		if got, _ := getFile(t, files, home+"/s-moved"); got != content {
			t.Fatal("sftp to scp move differs")
		}
		if _, ok := getFile(t, files, home+"/s-dst"); ok {
			t.Fatal("the source of a move still exists")
		}
	})
}

func TestIntegrationMinIO(t *testing.T) {
	conn, client := s3test.StartMinIO(t, "plain-bucket", "versioned-bucket")
	if err := client.EnableVersioning(context.Background(), "versioned-bucket"); err != nil {
		t.Fatal(err)
	}
	f := sshtest.NewFixture(t, "")
	run := func(in string, c map[string]string) plugintest.Outcome {
		cs := s3Case(f, nil, in)
		cs.Bundle[s3Ref] = c
		return exec_(t, cs)
	}
	dir := t.TempDir()

	t.Run("multipart upload and sha256 verify", func(t *testing.T) {
		content := strings.Repeat("0123456789abcdef", 70<<16) // 70 MiB: above the 64 MiB part size
		src := write(t, dir, "big", content)
		out := requireOK(t, run(mixed("copy", side{sshtest.NodeName, "sftp", src}, s3Side{"plain-bucket", "big"}, map[string]any{"verify": "sha256"}), conn))
		if out.Bytes != int64(len(content)) || out.SHA256 != sum(content) || !strings.Contains(out.ETag, "-") {
			t.Fatalf("output = %+v (a multipart ETag has a part count suffix)", out)
		}
	})

	t.Run("conditional put refuses an existing object atomically", func(t *testing.T) {
		src := write(t, dir, "small", "first")
		requireOK(t, run(mixed("copy", side{sshtest.NodeName, "sftp", src}, s3Side{"plain-bucket", "cond"}, nil), conn))
		// Skip the engine's own stat check by racing it: a second writer that
		// passes the check still gets 412 from the server.
		_, err := client.PutObject(context.Background(), "plain-bucket", "cond", strings.NewReader("other"), 5, putIfAbsent())
		if err == nil {
			t.Fatal("the server replaced an object despite If-None-Match")
		}
		requireKind(t, run(mixed("copy", side{sshtest.NodeName, "sftp", src}, s3Side{"plain-bucket", "cond"}, nil), conn), pluginsdk.KindValidation, "destination exists")
	})

	t.Run("versioned bucket reports the version id", func(t *testing.T) {
		src := write(t, dir, "v", "versioned")
		out := requireOK(t, run(mixed("copy", side{sshtest.NodeName, "sftp", src}, s3Side{"versioned-bucket", "v"}, nil), conn))
		if out.VersionID == "" {
			t.Fatalf("output = %+v; want a version id", out)
		}
	})

	t.Run("wrong credentials on a multipart upload are an auth error", func(t *testing.T) {
		bad := map[string]string{}
		for k, v := range conn {
			bad[k] = v
		}
		bad["secret_key"] = "wrong-secret-multipart"
		src := write(t, dir, "bigbad", strings.Repeat("0123456789abcdef", 70<<16))
		o := run(mixed("copy", side{sshtest.NodeName, "sftp", src}, s3Side{"plain-bucket", "bigbad"}, nil), bad)
		requireKind(t, o, pluginsdk.KindAuth, "")
	})

	t.Run("wrong credentials are an auth error and do not leak", func(t *testing.T) {
		bad := map[string]string{}
		for k, v := range conn {
			bad[k] = v
		}
		bad["secret_key"] = "wrong-secret-DO-NOT-LEAK-either"
		src := write(t, dir, "x", "x")
		o := run(mixed("copy", side{sshtest.NodeName, "sftp", src}, s3Side{"plain-bucket", "x"}, nil), bad)
		requireKind(t, o, pluginsdk.KindAuth, "")
		all := o.LogOutput + o.Result.Error.Message
		for _, l := range o.Emitter.Lines() {
			all += l.Line
		}
		if strings.Contains(all, "wrong-secret-DO-NOT-LEAK-either") || strings.Contains(all, s3test.MinIOPassword) {
			t.Fatal("a secret key reached the logs or the error")
		}
	})
}

func putIfAbsent() minio.PutObjectOptions {
	o := minio.PutObjectOptions{}
	o.SetMatchETagExcept("*")
	return o
}
