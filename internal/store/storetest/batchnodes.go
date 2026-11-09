package storetest

import (
	"context"
	"fmt"
	"testing"

	"github.com/denyszorinets/damson/internal/store"
)

func testBatchNodeLookups(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	const n = store.MaxBatch + 37 // more than one chunk
	var names, ids []string
	for i := range n {
		name := fmt.Sprintf("bn%04d", i)
		node, err := s.CreateNode(ctx, store.Node{
			ID: "bid-" + name, Name: name, Address: "10.0.0.1", Port: 22, Transport: "ssh", OS: "linux",
			HostKeyPolicy: "strict", Source: "manual", Enabled: true,
		}, t0)
		if err != nil {
			t.Fatal(err)
		}
		names, ids = append(names, name), append(ids, node.ID)
	}

	byName, err := s.GetNodesByNames(ctx, append([]string{"missing", "BN0001"}, names...)) // case matters
	if err != nil || len(byName) != n {
		t.Fatalf("GetNodesByNames: %d nodes, %v; want %d", len(byName), err, n)
	}
	if got := byName["bn0003"]; got.ID != "bid-bn0003" || got.Address != "10.0.0.1" || got.Port != 22 {
		t.Fatalf("node = %+v", got)
	}
	if _, ok := byName["BN0001"]; ok {
		t.Fatal("a name matched case-insensitively")
	}
	byID, err := s.GetNodesByIDs(ctx, append([]string{"nope"}, ids...))
	if err != nil || len(byID) != n || byID["bid-bn0500"].Name != "bn0500" {
		t.Fatalf("GetNodesByIDs: %d nodes, %v", len(byID), err)
	}
	if got, err := s.GetNodesByIDs(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("GetNodesByIDs(nil) = %v, %v", got, err)
	}
	if got, err := s.GetNodesByNames(ctx, []string{}); err != nil || len(got) != 0 {
		t.Fatalf("GetNodesByNames(empty) = %v, %v", got, err)
	}

	job, ver := mustJob(t, s, "batch-targets")
	tree, err := s.CreateRun(ctx, store.NewRun{
		JobID: job.ID, JobVersionID: ver.ID, Targets: targetsOf(30),
		Steps: []store.NewStepInstance{{Path: "a", Plugin: "shell", Execution: execSpec("p")}},
	}, t0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.RunTargetNodeIDs(ctx, tree.ID, []string{"n0002", "n0029", "N0002", "nope"})
	if err != nil || len(got) != 2 || got["n0002"] != "id-n0002" || got["n0029"] != "id-n0029" {
		t.Fatalf("RunTargetNodeIDs = %v, %v", got, err)
	}
	if got, err := s.RunTargetNodeIDs(ctx, "other-run", []string{"n0002"}); err != nil || len(got) != 0 {
		t.Fatalf("another run's targets = %v, %v", got, err)
	}
	// More names than one chunk, of which only the frozen ones are returned.
	many := make([]string, 0, n)
	for i := range n {
		many = append(many, fmt.Sprintf("n%04d", i))
	}
	if got, err := s.RunTargetNodeIDs(ctx, tree.ID, many); err != nil || len(got) != 30 {
		t.Fatalf("RunTargetNodeIDs over %d names = %d, %v; want 30", n, len(got), err)
	}
}
