package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

func testMetricsSnapshot(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	snap, err := s.MetricsSnapshot(ctx, t0.Add(-time.Minute))
	if err != nil {
		t.Fatalf("MetricsSnapshot on an empty store: %v", err)
	}
	if len(snap.RunsByStatus) != 0 || len(snap.Executions) != 0 || len(snap.Agents) != 0 || snap.LogDeletesPending != 0 {
		t.Fatalf("empty store snapshot = %+v", snap)
	}

	p := mustPool(t, s, "metrics")
	h := hello("m1", p.ID, "ctl-1")
	h.Capacity = 4
	if err := s.UpsertAgentOnHello(ctx, h, t0); err != nil {
		t.Fatal(err)
	}
	h2 := hello("m2", p.ID, "ctl-2")
	h2.Capacity = 6
	if err := s.UpsertAgentOnHello(ctx, h2, t0); err != nil {
		t.Fatal(err)
	}
	snap, err = s.MetricsSnapshot(ctx, t0.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Agents) != 1 || snap.Agents[0] != (store.PoolAgents{Pool: "metrics", Agents: 2, Slots: 10}) {
		t.Fatalf("agents = %+v; want 2 agents, 10 slots in pool metrics", snap.Agents)
	}

	// A disconnected agent no longer counts.
	if err := s.MarkAgentDisconnected(ctx, "m2", "ctl-2", h2.SessionSeq, t0); err != nil {
		t.Fatal(err)
	}
	snap, err = s.MetricsSnapshot(ctx, t0.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Agents) != 1 || snap.Agents[0].Agents != 1 || snap.Agents[0].Slots != 4 {
		t.Fatalf("agents after disconnect = %+v", snap.Agents)
	}
}

func testMetricsSnapshotWork(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	step := func(path, pool string) store.NewStepInstance {
		return store.NewStepInstance{Path: path, Plugin: "shell", Execution: &store.NewExecution{Pool: pool, ScheduledAt: t0}}
	}
	mustRun(t, s, "metrics-job", step("a", "pa"), step("b", "pa"), step("c", "pb"))
	got := claim(t, s, "pa", 1) // one pa execution becomes dispatched
	if len(got) != 1 {
		t.Fatalf("claimed %d; want 1", len(got))
	}
	snap, err := s.MetricsSnapshot(ctx, t0.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var runs int64
	for _, n := range snap.RunsByStatus {
		runs += n
	}
	if runs != 1 {
		t.Fatalf("runs = %+v; want 1 in total", snap.RunsByStatus)
	}
	want := map[store.PoolExecutions]bool{
		{Pool: "pa", State: "queued", Count: 1}:     true,
		{Pool: "pa", State: "dispatched", Count: 1}: true,
		{Pool: "pb", State: "queued", Count: 1}:     true,
	}
	if len(snap.Executions) != len(want) {
		t.Fatalf("executions = %+v", snap.Executions)
	}
	for _, e := range snap.Executions {
		if !want[e] {
			t.Fatalf("unexpected %+v in %+v", e, snap.Executions)
		}
	}
}

func testMetricsStaleAgents(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	p := mustPool(t, s, "stale")
	if err := s.UpsertAgentOnHello(ctx, hello("s1", p.ID, "ctl-1"), t0); err != nil {
		t.Fatal(err)
	}
	snap, err := s.MetricsSnapshot(ctx, t0.Add(time.Minute)) // last_seen t0 is older than the cutoff
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Agents) != 0 {
		t.Fatalf("an agent unseen since the cutoff still counts: %+v", snap.Agents)
	}
}
