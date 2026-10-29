package dispatch_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

const (
	secretMarker = "S3CR3T-MARKER-VALUE"
	nodeSpec     = `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"lin"},
"spec":{"steps":[{"id":"s1","plugin":"nodedemo","with":{"node":"n1","token":"secret://dev/token"}}]}}`
)

type fakeSources struct {
	mu       sync.Mutex
	nodeErr  error
	tokenErr error
	secrets  int
}

func (f *fakeSources) Node(_ context.Context, _, name string) (pluginsdk.NodeInfo, error) {
	if name != "n1" {
		return pluginsdk.NodeInfo{}, dispatch.ErrUnknownNode
	}
	return pluginsdk.NodeInfo{Name: name, Address: "10.0.0.7", Credential: "secret://dev/cred", HostKeyPolicy: "strict"}, f.nodeErr
}

func (f *fakeSources) Secret(_ context.Context, _, ref string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets++
	switch ref {
	case "secret://dev/cred":
		return map[string]string{"username": "damson", "private_key": secretMarker + "-KEY"}, nil
	case "secret://dev/token":
		return map[string]string{"value": secretMarker}, f.tokenErr
	}
	return nil, dispatch.ErrUnknownSecret
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func withSources(src *fakeSources) func(*dispatch.Config) {
	return func(c *dispatch.Config) { c.Nodes, c.Secrets = src, src }
}

// The node and every referenced secret reach the agent, and no value reaches
// a log, the execution error or the stored step input.
func TestNodeAndSecretsDelivered(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		logs := &lockedBuf{}
		log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		c := startClusterLog(t, st, nodeSpec, log, withSources(&fakeSources{}))
		id := c.triggerInput("nodedemo", `{"node":"n1","token":"secret://dev/token"}`)
		tree := c.waitRun(id, store.RunSucceeded)
		// The agent masks every delivered value, the username included.
		waitLog(t, st, tree.Steps[0].Executions[0].ID, "node 10.0.0.7 user *** token-len "+strconv.Itoa(len(secretMarker)))
		if strings.Contains(logs.String(), secretMarker) {
			t.Fatalf("a secret value reached the controller log: %s", logs.String())
		}
		n := tree.Steps[0]
		if strings.Contains(string(n.Input), secretMarker) || strings.Contains(n.Error, secretMarker) || strings.Contains(n.Executions[0].Error, secretMarker) {
			t.Fatal("a secret value was stored")
		}
	})
}

// An unknown node or secret fails the execution for good, naming only the
// node or ref.
func TestUnknownNodeOrSecretFailsExecution(t *testing.T) {
	for _, tc := range []struct{ input, names string }{
		{`{"node":"missing","token":"secret://dev/token"}`, "missing"},
		{`{"node":"n1","token":"secret://dev/nope"}`, "secret://dev/nope"},
	} {
		t.Run(tc.names, func(t *testing.T) {
			forEachBackend(t, func(t *testing.T, st store.Store) {
				c := startCluster(t, st, nodeSpec, withSources(&fakeSources{}))
				tree := c.waitRun(c.triggerInput("nodedemo", tc.input), store.RunFailed)
				ex := tree.Steps[0].Executions[0]
				if ex.State != store.ExecFailed || strings.Contains(ex.Error, secretMarker) || !strings.Contains(ex.Error, tc.names) {
					t.Fatalf("execution = %s %q; want failed naming %q", ex.State, ex.Error, tc.names)
				}
			})
		})
	}
}

// A transient source error requeues instead of failing the step.
func TestTransientSourceErrorRequeues(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		c := startCluster(t, st, nodeSpec, withSources(&fakeSources{tokenErr: errors.New("disk on fire")}))
		id := c.triggerInput("nodedemo", `{"node":"n1","token":"secret://dev/token"}`)
		time.Sleep(600 * time.Millisecond)
		tree, err := st.GetRunTree(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if ex := tree.Steps[0].Executions[0]; ex.State != store.ExecQueued {
			t.Fatalf("execution = %s; want queued", ex.State)
		}
	})
}

// Without sources a step gets no node: the plugin reports it missing.
func TestNoSourcesNoNodes(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		c := startCluster(t, st, nodeSpec)
		c.waitRun(c.triggerInput("nodedemo", `{"node":"n1","token":"x"}`), store.RunFailed)
	})
}
