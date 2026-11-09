package storetest

import (
	"context"
	"testing"

	"github.com/denyszorinets/damson/internal/store"
)

func testMetricsSnapshot(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	snap, err := s.MetricsSnapshot(ctx)
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
	snap, err = s.MetricsSnapshot(ctx)
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
	snap, err = s.MetricsSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Agents) != 1 || snap.Agents[0].Agents != 1 || snap.Agents[0].Slots != 4 {
		t.Fatalf("agents after disconnect = %+v", snap.Agents)
	}
}
