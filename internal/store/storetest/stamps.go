package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/denyszorinets/damson/internal/store"
)

func testStamps(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	e := claimedOne(t, s)

	rs0, err := s.GetRunStamp(ctx, e.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rs0.Steps != 1 || rs0.Execs != 1 || rs0.ExecsDispatched != 1 {
		t.Fatalf("stamp = %+v", rs0)
	}
	if rs1, _ := s.GetRunStamp(ctx, e.RunID); rs1 != rs0 {
		t.Fatalf("stamp changed without a write: %+v vs %+v", rs1, rs0)
	}
	if err := s.MarkExecutionRunning(ctx, e.ID, e.AttemptID, claimNow); err != nil {
		t.Fatal(err)
	}
	rs2, _ := s.GetRunStamp(ctx, e.RunID)
	if rs2 == rs0 || rs2.ExecsRunning != 1 || rs2.ExecsDispatched != 0 {
		t.Fatalf("stamp after running = %+v", rs2)
	}

	ls0, err := s.GetLogStamp(ctx, e.ID)
	if err != nil || ls0.State != store.ExecRunning || ls0.Chunks != 0 || ls0.MaxSeq != 0 {
		t.Fatalf("log stamp = %+v, %v", ls0, err)
	}
	if _, err := s.InsertLogChunk(ctx, e.AttemptID, chunk(e.ID, 5, 9, "k/5-9"), claimNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertLogChunk(ctx, e.AttemptID, chunk(e.ID, 1, 4, "k/1-4"), claimNow); err != nil {
		t.Fatal(err)
	}
	ls1, _ := s.GetLogStamp(ctx, e.ID)
	if ls1.Chunks != 2 || ls1.MaxSeq != 9 {
		t.Fatalf("log stamp after chunks = %+v", ls1)
	}
	if _, err := s.CompleteExecution(ctx, store.Completion{ExecutionID: e.ID, AttemptID: e.AttemptID, State: store.ExecSucceeded}, claimNow); err != nil {
		t.Fatal(err)
	}
	ls2, _ := s.GetLogStamp(ctx, e.ID)
	if ls2 == ls1 || !ls2.State.Terminal() {
		t.Fatalf("log stamp after completion = %+v", ls2)
	}
	rs3, _ := s.GetRunStamp(ctx, e.RunID)
	if rs3 == rs2 || rs3.ExecsDone != 1 {
		t.Fatalf("run stamp after completion = %+v", rs3)
	}

	if _, err := s.GetRunStamp(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetRunStamp(nope) = %v; want ErrNotFound", err)
	}
	if _, err := s.GetLogStamp(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetLogStamp(nope) = %v; want ErrNotFound", err)
	}
}
