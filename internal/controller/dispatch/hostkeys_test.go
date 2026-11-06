package dispatch_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

type recordKeys struct {
	mu   sync.Mutex
	seen [][3]string
}

func (r *recordKeys) ObserveHostKey(_ context.Context, runID, _, _, name, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, [3]string{runID, name, key})
	return nil
}

func (r *recordKeys) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

// singleNodePlugins resolves every plugin to a manifest with the given target mode.
type singleNodePlugins struct{ mode pluginsdk.TargetMode }

func (p singleNodePlugins) Resolve(ref string, _ pluginreg.Filter) (pluginreg.Resolution, error) {
	return pluginreg.Resolution{Manifest: pluginsdk.Manifest{Name: ref, TargetMode: p.mode}}, nil
}

func hostKeyReport(t *testing.T, attempt, node string) (protocol.HostKeyObserved, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))
	return protocol.HostKeyObserved{AttemptID: attempt, Node: node, KeyType: sp.Type(), HostKey: key, Fingerprint: ssh.FingerprintSHA256(sp)}, key
}

// node.hostkey_observed is fenced like step.complete and checked against the
// step it names; only a valid report for the targeted node is recorded.
func TestHostKeyObservedFencing(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		now := time.Now()
		rec := &recordKeys{}
		d := newBareWith(t, st, func() time.Time { return now }, func(c *dispatch.Config) { c.HostKeys = rec; c.Plugins = singleNodePlugins{pluginsdk.TargetSingleNode} })
		_, jv, err := st.CreateJob(ctx, store.NewJob{ProjectID: store.DefaultProjectID, Name: "hk", Spec: json.RawMessage(twoStepSpec), Checksum: "s"}, t0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateRun(ctx, store.NewRun{JobID: jv.JobID, JobVersionID: jv.ID, Steps: []store.NewStepInstance{
			{Path: "s1", Plugin: "ssh", Input: json.RawMessage(`{"node":"web-1"}`), Execution: &store.NewExecution{Pool: "default", ScheduledAt: now}}}}, now); err != nil {
			t.Fatal(err)
		}
		got, err := st.ClaimExecutions(ctx, store.ClaimParams{AgentID: "a1", ControllerID: "ctl-1", Pools: []string{"default"}, Limit: 1, LeaseTTL: time.Minute}, now)
		if err != nil || len(got) != 1 {
			t.Fatalf("claim = %v, %v", got, err)
		}
		ex := got[0]
		if err := st.MarkExecutionRunning(ctx, ex.ID, ex.AttemptID, now); err != nil {
			t.Fatal(err)
		}

		// None of these may be recorded.
		good, key := hostKeyReport(t, ex.AttemptID, "web-1")
		bad := map[string]struct {
			agent string
			m     protocol.HostKeyObserved
		}{
			"unknown attempt": {"a1", func() protocol.HostKeyObserved { m := good; m.AttemptID = "nope"; return m }()},
			"another agent":   {"a2", good},
			"another node":    {"a1", func() protocol.HostKeyObserved { m := good; m.Node = "db-1"; return m }()},
			"wrong fingerprint": {"a1", func() protocol.HostKeyObserved {
				m := good
				m.Fingerprint = "SHA256:AAAA"
				return m
			}()},
			"wrong type": {"a1", func() protocol.HostKeyObserved { m := good; m.KeyType = "ssh-rsa"; return m }()},
			"unparsable": {"a1", func() protocol.HostKeyObserved { m := good; m.HostKey = "not a key"; return m }()},
		}
		for name, c := range bad {
			d.HostKeyObserved(ctx, c.agent, c.m)
			if rec.count() != 0 {
				t.Fatalf("%s was recorded", name)
			}
		}

		d.HostKeyObserved(ctx, "a1", good)
		if rec.count() != 1 || rec.seen[0] != [3]string{ex.RunID, "web-1", key} {
			t.Fatalf("recorded = %v", rec.seen)
		}

		// A finished attempt can no longer report.
		if _, err := d.Complete(ctx, "a1", protocol.StepComplete{AttemptID: ex.AttemptID, Status: protocol.StatusSucceeded}); err != nil {
			t.Fatal(err)
		}
		d.HostKeyObserved(ctx, "a1", good)
		if rec.count() != 1 {
			t.Fatal("a report after completion was recorded")
		}
	})
}

// A step whose plugin does not target a single node cannot report a host key,
// and the worker applies reports without blocking the caller.
func TestHostKeyObservedPluginAndWorker(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		now := time.Now()
		rec := &recordKeys{}
		mode := pluginsdk.TargetNone
		d := newBareWith(t, st, func() time.Time { return now }, func(c *dispatch.Config) {
			c.HostKeys = rec
			c.Plugins = &modePlugins{mode: &mode}
		})
		_, jv, err := st.CreateJob(ctx, store.NewJob{ProjectID: store.DefaultProjectID, Name: "hk2", Spec: json.RawMessage(twoStepSpec), Checksum: "s"}, t0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateRun(ctx, store.NewRun{JobID: jv.JobID, JobVersionID: jv.ID, Steps: []store.NewStepInstance{
			{Path: "s1", Plugin: "shell", Input: json.RawMessage(`{"node":"web-1"}`), Execution: &store.NewExecution{Pool: "default", ScheduledAt: now}}}}, now); err != nil {
			t.Fatal(err)
		}
		got, err := st.ClaimExecutions(ctx, store.ClaimParams{AgentID: "a1", ControllerID: "ctl-1", Pools: []string{"default"}, Limit: 1, LeaseTTL: time.Minute}, now)
		if err != nil || len(got) != 1 {
			t.Fatalf("claim = %v, %v", got, err)
		}
		report, _ := hostKeyReport(t, got[0].AttemptID, "web-1")
		d.HostKeyObserved(ctx, "a1", report)
		if rec.count() != 0 {
			t.Fatal("a plugin that targets no node reported a host key")
		}
		mode = pluginsdk.TargetSingleNode
		if err := d.Start(ctx); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = d.Stop(ctx) }()
		d.HostKeyObserved(ctx, "a1", report)
		deadline := time.Now().Add(5 * time.Second)
		for rec.count() == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if rec.count() != 1 {
			t.Fatalf("the worker recorded %d reports; want 1", rec.count())
		}
	})
}

type modePlugins struct{ mode *pluginsdk.TargetMode }

func (p *modePlugins) Resolve(ref string, _ pluginreg.Filter) (pluginreg.Resolution, error) {
	return pluginreg.Resolution{Manifest: pluginsdk.Manifest{Name: ref, TargetMode: *p.mode}}, nil
}

// nodesPlugins resolves every plugin to a TargetNodes manifest.
type nodesPlugins struct{}

func (nodesPlugins) Resolve(ref string, _ pluginreg.Filter) (pluginreg.Resolution, error) {
	return pluginreg.Resolution{Manifest: pluginsdk.Manifest{Name: ref, TargetMode: pluginsdk.TargetNodes,
		NodePaths: []string{"/source/node/name", "/destination/node/name"}}}, nil
}

// For a TargetNodes step the fence is membership: a report for the source or
// the destination is recorded, one for a third node or an s3 endpoint is not.
func TestHostKeyObservedFencingNodesMode(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		now := time.Now()
		rec := &recordKeys{}
		d := newBareWith(t, st, func() time.Time { return now }, func(c *dispatch.Config) { c.HostKeys = rec; c.Plugins = nodesPlugins{} })
		_, jv, err := st.CreateJob(ctx, store.NewJob{ProjectID: store.DefaultProjectID, Name: "hk3", Spec: json.RawMessage(twoStepSpec), Checksum: "s"}, t0)
		if err != nil {
			t.Fatal(err)
		}
		input := `{"source":{"node":{"name":"web-1"}},"destination":{"s3":{"bucket":"b"}}}`
		if _, err := st.CreateRun(ctx, store.NewRun{JobID: jv.JobID, JobVersionID: jv.ID, Steps: []store.NewStepInstance{
			{Path: "s1", Plugin: "transfer", Input: json.RawMessage(input), Execution: &store.NewExecution{Pool: "default", ScheduledAt: now}}}}, now); err != nil {
			t.Fatal(err)
		}
		got, err := st.ClaimExecutions(ctx, store.ClaimParams{AgentID: "a1", ControllerID: "ctl-1", Pools: []string{"default"}, Limit: 1, LeaseTTL: time.Minute}, now)
		if err != nil || len(got) != 1 {
			t.Fatalf("claim = %v, %v", got, err)
		}
		ex := got[0]
		if err := st.MarkExecutionRunning(ctx, ex.ID, ex.AttemptID, now); err != nil {
			t.Fatal(err)
		}
		for _, node := range []string{"db-1", "s3", ""} {
			m, _ := hostKeyReport(t, ex.AttemptID, node)
			d.HostKeyObserved(ctx, "a1", m)
		}
		if rec.count() != 0 {
			t.Fatalf("a report for a node outside the step was recorded: %v", rec.seen)
		}
		m, key := hostKeyReport(t, ex.AttemptID, "web-1")
		d.HostKeyObserved(ctx, "a1", m)
		if rec.count() != 1 || rec.seen[0] != [3]string{ex.RunID, "web-1", key} {
			t.Fatalf("recorded = %v", rec.seen)
		}
	})
}
