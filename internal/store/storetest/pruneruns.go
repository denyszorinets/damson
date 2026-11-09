package storetest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

func pruneRuns(ctx context.Context, t *testing.T, s store.Store, before time.Time) store.RunPrune {
	t.Helper()
	var got store.RunPrune
	err := s.WithTx(ctx, func(tx store.Tx) error {
		var err error
		got, err = tx.PruneRuns(ctx, before, 10, claimNow)
		return err
	})
	if err != nil {
		t.Fatalf("PruneRuns: %v", err)
	}
	return got
}

func testPruneRuns(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	old := claimNow.Add(-48 * time.Hour)
	cutoff := claimNow.Add(-24 * time.Hour)

	// A finished run with logs in an external backend.
	e := claimedOne(t, s)
	key := store.LogDataPrefix(e.ID) + "1-10"
	ext := chunk(e.ID, 1, 10, key)
	ext.Backend = "s3"
	if _, err := s.InsertLogChunk(ctx, e.AttemptID, ext, claimNow); err != nil {
		t.Fatal(err)
	}
	if err := s.PutLogData(ctx, key, []byte("line"), claimNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteExecution(ctx, store.Completion{ExecutionID: e.ID, AttemptID: e.AttemptID, State: store.ExecFailed}, claimNow); err != nil {
		t.Fatal(err)
	}
	run, err := s.GetRun(ctx, e.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateRunStatus(ctx, run.ID, run.Version, store.RunFailed, old); err != nil {
		t.Fatal(err)
	}

	// An old finished run whose execution is still queued must stay.
	stuck, stuckTree := mustRun(t, s, "stuck")
	_ = stuck
	if _, err := s.UpdateRunStatus(ctx, stuckTree.ID, stuckTree.Version, store.RunCanceled, old.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A recently finished run, and a run that is still going, must stay.
	_, recent := mustRun(t, s, "recent")
	if _, err := s.UpdateRunStatus(ctx, recent.ID, recent.Version, store.RunCanceled, claimNow); err != nil {
		t.Fatal(err)
	}
	_, running := mustRun(t, s, "running")

	if err := s.WithTx(context.Background(), func(tx store.Tx) error {
		_, err := tx.PruneRuns(context.Background(), cutoff, 10, claimNow)
		return err
	}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("prune without an actor: %v; want ErrInvalid", err)
	}
	if _, err := s.GetRun(ctx, run.ID); err != nil {
		t.Fatalf("a refused prune removed the run: %v", err)
	}

	got := pruneRuns(ctx, t, s, cutoff)
	if got.Runs != 1 || got.Executions != 1 || got.Chunks != 1 || got.Objects != 1 || got.Queued != 1 {
		t.Fatalf("prune = %+v; want 1 run, 1 execution, 1 chunk, 1 object, 1 queued delete", got)
	}
	if _, err := s.GetRun(ctx, run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("pruned run: %v; want ErrNotFound", err)
	}
	if _, err := s.GetExecution(ctx, e.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("pruned execution: %v; want ErrNotFound", err)
	}
	if _, err := s.GetLogData(ctx, key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("log data left: %v", err)
	}
	if due, _ := s.ListDueLogObjectDeletes(ctx, claimNow, 10); len(due) != 1 || due[0].Prefix != store.LogDataPrefix(e.ID) {
		t.Fatalf("queued deletes = %+v; want the execution prefix", due)
	}
	for name, id := range map[string]string{"stuck": stuckTree.ID, "recent": recent.ID, "running": running.ID} {
		if _, err := s.GetRun(ctx, id); err != nil {
			t.Errorf("run %s was pruned: %v", name, err)
		}
	}
	if again := pruneRuns(ctx, t, s, cutoff); again.Runs != 0 {
		t.Fatalf("second prune = %+v; want nothing", again)
	}
	evs, err := s.ListAuditEvents(ctx, store.AuditFilter{TargetType: store.AuditTargetInstance, Target: "runs"}, 10)
	if err != nil || len(evs) != 1 || evs[0].Action != store.ActionRunPrune || !strings.Contains(string(evs[0].Details), `"runs":1`) {
		t.Fatalf("events = %+v, %v", evs, err)
	}
	if strings.Contains(string(evs[0].Details), "line") {
		t.Fatal("log content in the audit event")
	}
}
