//go:build integration && unix

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	gossh "golang.org/x/crypto/ssh"

	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/store"
)

const (
	sshUser = "damson"
	sshPort = "2222/tcp"
	// Pinned like the ssh plugin's own integration test.
	sshImage = "lscr.io/linuxserver/openssh-server:version-10.3_p1-r1"
)

// e2eSSH is a running sshd with one authorized key and the dev nodes file
// that points at it.
type e2eSSH struct {
	nodesFile, keyPEM, token string
}

func startE2ESSH(t *testing.T) *e2eSSH {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(block))
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        sshImage,
			ExposedPorts: []string{sshPort},
			Env: map[string]string{
				"PUID": "1000", "PGID": "1000", "TZ": "UTC", "USER_NAME": sshUser,
				"PUBLIC_KEY":      strings.TrimSpace(string(gossh.MarshalAuthorizedKey(sshPub))),
				"PASSWORD_ACCESS": "false", "SUDO_ACCESS": "false",
			},
			WaitingFor: wait.ForListeningPort(sshPort).WithStartupTimeout(2 * time.Minute),
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
	port, err := ctr.MappedPort(ctx, sshPort)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	o := &e2eSSH{keyPEM: keyPEM, token: "tok-" + fmt.Sprint(time.Now().UnixNano())}
	keyFile := writeFile(t, dir, "id_ed25519", keyPEM)
	tokenFile := writeFile(t, dir, "token", o.token)
	nodes := map[string]any{
		"version": 1,
		"nodes": map[string]any{"sshd": map[string]any{
			"address": host, "port": port.Num(), "transport": "ssh", "os": "linux",
			"host_key_policy": "strict", "host_key": learnHostKey(t, host, port.Port()),
			"credential": "secret://dev/sshd",
		}},
		"secrets": map[string]any{
			"secret://dev/sshd":  map[string]string{"username": sshUser, "private_key_file": keyFile},
			"secret://dev/token": map[string]string{"value_file": tokenFile},
		},
	}
	raw, _ := json.Marshal(nodes)
	o.nodesFile = writeFile(t, dir, "nodes.json", string(raw))
	return o
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// learnHostKey does a handshake only to capture the server's host key, which
// the node then pins with the strict policy.
func learnHostKey(t *testing.T, host, port string) string {
	t.Helper()
	var line string
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 5*time.Second)
		if err == nil {
			cfg := &gossh.ClientConfig{
				User: "probe", Timeout: 5 * time.Second,
				HostKeyCallback: func(_ string, _ net.Addr, k gossh.PublicKey) error {
					line = strings.TrimSpace(string(gossh.MarshalAuthorizedKey(k)))
					return nil
				},
			}
			c, _, _, _ := gossh.NewClientConn(conn, host, cfg) // auth fails; the key was seen
			if c != nil {
				_ = c.Close()
			}
			_ = conn.Close()
			if line != "" {
				return line
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("could not learn the sshd host key")
	return ""
}

// e2eCluster is a controller (plus a separate agent on Postgres) on be.
type e2eCluster struct {
	base  string
	logs  []*syncBuf
	stops []func()
}

func startE2E(t *testing.T, be adminBackend, o *e2eSSH) *e2eCluster {
	t.Helper()
	env := append(adminEnv(t, be, true), "DAMSON_CONTROLLER__LISTEN=127.0.0.1:0", "DAMSON_AGENT__ALLOW_INSECURE_HOST_KEYS=false")
	c := &e2eCluster{}
	launch := func(args, env []string) *syncBuf {
		ctx, cancel := context.WithCancel(context.Background())
		var errb syncBuf
		done := make(chan int, 1)
		go func() { done <- run(ctx, args, io.Discard, &errb, env) }()
		c.logs = append(c.logs, &errb)
		c.stops = append(c.stops, func() { stopRole(t, cancel, done, &errb) })
		return &errb
	}
	if be.driver == config.DriverSQLite {
		c.base = "http://" + listenAddr(t, launch([]string{"dev", "--dev-nodes", o.nodesFile}, env))
	} else {
		mustAdmin(t, env, "pool", "create", "default")
		token := strings.TrimSpace(mustAdmin(t, env, "token", "create", "--pool", "default", "--name", "t"))
		addr := listenAddr(t, launch([]string{"controller", "--insecure-dev", "--dev-nodes", o.nodesFile}, env))
		c.base = "http://" + addr
		launch([]string{"agent"}, append(append([]string(nil), env...), "DAMSON_AGENT__CONTROLLER_URL=ws://"+addr, "DAMSON_AGENT__TOKEN="+token, "DAMSON_AGENT__SLOTS=2"))
	}
	st := openStore(t, be, env)
	waitAgents(t, st, "agent to connect", func(as []store.Agent) bool { return len(as) >= 1 && as[0].ControllerID != "" })
	t.Cleanup(func() {
		for i := len(c.stops) - 1; i >= 0; i-- {
			c.stops[i]()
		}
	})
	return c
}

func (c *e2eCluster) api(t *testing.T, method, path, body string, out any) {
	t.Helper()
	req, err := http.NewRequest(method, c.base+"/api/v1"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s = %d: %s", method, path, resp.StatusCode, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("decoding %s: %v", data, err)
		}
	}
}

// sseLines returns the log lines of an execution from its SSE stream.
func (c *e2eCluster) sseLines(t *testing.T, execID string) string {
	t.Helper()
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(c.base + "/api/v1/executions/" + execID + "/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out bytes.Buffer
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if line, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
			out.WriteString(line + "\n")
		}
	}
	return out.String()
}

const e2eSpec = `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"hello-ssh"},
"spec":{"steps":[{"id":"hello","plugin":"ssh","with":{"node":"sshd","command":"echo hello-from-sshd; echo t=$TOKEN","env":{"TOKEN":"secret://dev/token"}}}]}}`

// TestE2ESSHRun creates a job over the API, triggers it and checks the step
// outputs, the masked log and the run status; SQLite runs `damson dev`,
// Postgres a controller with a separate agent.
func TestE2ESSHRun(t *testing.T) {
	forEachBackend(t, func(t *testing.T, be adminBackend) {
		o := startE2ESSH(t)
		c := startE2E(t, be, o)

		var job struct {
			Job struct{ ID string } `json:"job"`
		}
		c.api(t, http.MethodPost, "/jobs", `{"spec":`+e2eSpec+`}`, &job)
		var run struct {
			ID string `json:"id"`
		}
		c.api(t, http.MethodPost, "/jobs/"+job.Job.ID+"/runs", "", &run)

		var detail struct {
			Status string `json:"status"`
			Steps  []struct {
				Status     string          `json:"status"`
				Outputs    json.RawMessage `json:"outputs"`
				Executions []struct {
					ID string `json:"id"`
				} `json:"executions"`
			} `json:"steps"`
		}
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			c.api(t, http.MethodGet, "/runs/"+run.ID, "", &detail)
			if detail.Status != "pending" && detail.Status != "running" {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if detail.Status != "succeeded" || len(detail.Steps) != 1 || detail.Steps[0].Status != "succeeded" {
			t.Fatalf("run = %+v; logs: %s", detail, c.logs[0].String())
		}
		// The agent replaces a whole string output that contains a secret with
		// ***, so stdout_tail is not checked for text, only for the token.
		var out struct {
			ExitCode   int    `json:"exit_code"`
			StdoutTail string `json:"stdout_tail"`
		}
		if err := json.Unmarshal(detail.Steps[0].Outputs, &out); err != nil || out.ExitCode != 0 {
			t.Fatalf("outputs = %s (%v)", detail.Steps[0].Outputs, err)
		}
		if strings.Contains(out.StdoutTail, o.token) {
			t.Fatal("the secret reached the step outputs unmasked")
		}

		lines := c.sseLines(t, detail.Steps[0].Executions[0].ID)
		if !strings.Contains(lines, "hello-from-sshd") || !strings.Contains(lines, "t=***") {
			t.Fatalf("log = %s", lines)
		}
		if strings.Contains(lines, o.token) {
			t.Fatal("the secret reached the stored log")
		}

		// No process log may hold the token or key material.
		keyBody := strings.Split(strings.TrimSpace(o.keyPEM), "\n")[1]
		for _, l := range c.logs {
			if s := l.String(); strings.Contains(s, o.token) || strings.Contains(s, keyBody) {
				t.Fatalf("a secret reached a process log: %s", s)
			}
		}
	})
}
