package logs

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
)

// failingStore injects errors into the log path; a nil error passes through.
type failingStore struct {
	store.Store
	getErr, putErr, insertErr error
}

func (s failingStore) GetExecutionByAttempt(ctx context.Context, a string) (store.Execution, error) {
	if s.getErr != nil {
		return store.Execution{}, s.getErr
	}
	return s.Store.GetExecutionByAttempt(ctx, a)
}

func (s failingStore) PutLogData(ctx context.Context, k string, d []byte, now time.Time) error {
	if s.putErr != nil {
		return s.putErr
	}
	return s.Store.PutLogData(ctx, k, d, now)
}

func (s failingStore) InsertLogChunk(ctx context.Context, a string, c store.NewLogChunk, now time.Time) (store.LogChunk, error) {
	if s.insertErr != nil {
		return store.LogChunk{}, s.insertErr
	}
	return s.Store.InsertLogChunk(ctx, a, c, now)
}

func TestFlush_ByTime(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFx(t, st)
		f.mustSend(1, 0, "a", "b")

		f.clock.advance(DefaultFlushInterval - time.Second)
		f.in.duePass(context.Background())
		assert.Empty(t, f.chunks(), "nothing is due before the flush interval")
		assert.Empty(t, f.acks.all())

		f.clock.advance(time.Second)
		f.in.duePass(context.Background())
		assert.Equal(t, [][2]int64{{1, 2}}, f.ranges())
		assert.EqualValues(t, 2, f.acks.last(f.exec.AttemptID))
	})
}

func TestFlush_BySize(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFx(t, st, func(c *Config) { c.FlushBytes = 1000 })
		f.mustSend(1, 0, numbered(1, 3)...)
		f.in.duePass(context.Background())
		assert.Empty(t, f.chunks())

		big := make([]string, 5)
		for i := range big {
			big[i] = string(make([]byte, 300))
		}
		f.mustSend(4, 0, big...)
		f.in.duePass(context.Background()) // no clock advance: size alone triggers
		assert.Equal(t, [][2]int64{{1, 8}}, f.ranges())
		assert.EqualValues(t, 8, f.acks.last(f.exec.AttemptID))
	})
}

func TestFlush_LoopWakesOnSizeAndTick(t *testing.T) {
	f := newFx(t, openSQLite(t), func(c *Config) { c.FlushBytes = 100 })
	f.start()
	f.mustSend(1, 0, string(make([]byte, 200)))
	waitFor(t, "size-triggered chunk", func() bool { return len(f.chunks()) == 1 })

	f.mustSend(2, 0, "small")
	f.clock.advance(DefaultFlushInterval)
	f.clock.tick()
	waitFor(t, "time-triggered chunk", func() bool { return len(f.chunks()) == 2 })
	waitFor(t, "ack", func() bool { return f.acks.last(f.exec.AttemptID) == 2 })
}

func TestFlush_ConflictIsAlreadyPersisted(t *testing.T) {
	tests := []struct {
		name       string
		existing   [][2]int64
		send       int // lines 1..send
		wantRanges [][2]int64
	}{
		{"exact range exists", [][2]int64{{1, 5}}, 5, [][2]int64{{1, 5}}},
		{"shorter chunk exists", [][2]int64{{1, 3}}, 5, [][2]int64{{1, 3}, {4, 5}}},
		{"chain of earlier chunks", [][2]int64{{1, 2}, {3, 4}}, 6, [][2]int64{{1, 2}, {3, 4}, {5, 6}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			forEachBackend(t, func(t *testing.T, st store.Store) {
				f := newFx(t, st)
				for _, r := range tc.existing { // persisted earlier, ack lost
					_, err := st.InsertLogChunk(context.Background(), f.exec.AttemptID, store.NewLogChunk{
						ExecutionID: f.exec.ID, SeqFrom: r[0], SeqTo: r[1], Backend: "db", ObjectKey: ObjectKey(f.exec.ID, r[0], r[1]),
					}, t0)
					require.NoError(t, err)
				}
				f.mustSend(1, 0, numbered(1, tc.send)...)
				f.flush()
				assert.Equal(t, tc.wantRanges, f.ranges())
				assert.EqualValues(t, tc.send, f.acks.last(f.exec.AttemptID))
				assert.NotZero(t, f.in.Stats().ChunkConflicts)
			})
		})
	}
}

func TestFlush_StaleAttemptDiscardsBuffer(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFx(t, st)
		f.mustSend(1, 0, "a", "b")
		f.reclaim() // the attempt is superseded between ingest and flush
		require.NoError(t, f.in.Flush(context.Background(), f.exec.ID))
		assert.Empty(t, f.chunks())
		assert.Empty(t, f.acks.all(), "a stale attempt is never acknowledged as persisted")
		assert.EqualValues(t, 2, f.in.Stats().LinesDiscardedStale)
		_, err := f.in.Subscribe(context.Background(), f.exec.ID, 0)
		assert.ErrorIs(t, err, ErrNoLiveTail)
	})
}

func TestAck_OnlyAfterChunkCommit(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFx(t, st)
		var mu sync.Mutex
		var violations []string
		f.acks.onAck = func(a protocol.LogAck) {
			cs, err := st.ListLogChunks(context.Background(), f.exec.ID, int64(a.Seq), int64(a.Seq), 4)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || len(cs) == 0 {
				violations = append(violations, "ack "+strconv.FormatUint(a.Seq, 10)+" sent without a committed chunk covering it")
			}
		}
		f.mustSend(1, 0, "a", "b", "c")
		f.in.sendAcks(context.Background())
		assert.Empty(t, f.acks.all(), "no ack before the chunk is committed")

		f.flush()
		assert.EqualValues(t, 3, f.acks.last(f.exec.AttemptID))
		mu.Lock()
		defer mu.Unlock()
		assert.Empty(t, violations)
	})
}

func TestAck_NotSentWhenCommitFails(t *testing.T) {
	tests := []struct {
		name string
		fail func(*failingStore)
	}{
		{"object write fails", func(s *failingStore) { s.putErr = errors.New("put failed") }},
		{"manifest insert fails", func(s *failingStore) { s.insertErr = errors.New("insert failed") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t, openSQLite(t))
			f.mustSend(1, 0, "a", "b")
			bad := failingStore{Store: f.st}
			tc.fail(&bad)
			f.in.cfg.Store = bad
			require.Error(t, f.in.Flush(context.Background(), f.exec.ID))
			assert.Empty(t, f.acks.all())
			assert.Empty(t, f.chunks())
			assert.EqualValues(t, 1, f.in.Stats().FlushErrors)

			// lines are kept and written once the store recovers
			f.in.cfg.Store = f.st
			f.flush()
			assert.Equal(t, [][2]int64{{1, 2}}, f.ranges())
			assert.EqualValues(t, 2, f.acks.last(f.exec.AttemptID))
		})
	}
}

func TestFlush_PartialFailureAcksOnlyCommittedRun(t *testing.T) {
	f := newFx(t, openSQLite(t))
	f.mustSend(1, 0, "a", "b")
	f.mustSend(5, 0, "e") // hole 3-4 makes a second chunk
	flaky := &flakyInsert{Store: f.st, failFrom: 5}
	f.in.cfg.Store = flaky
	require.Error(t, f.in.Flush(context.Background(), f.exec.ID))
	assert.Equal(t, [][2]int64{{1, 2}}, f.ranges())
	assert.EqualValues(t, 4, f.acks.last(f.exec.AttemptID), "ack stops before the uncommitted run (the hole 3-4 counts as a gap)")
	flaky.failFrom = 0
	f.flush()
	assert.Equal(t, [][2]int64{{1, 2}, {5, 5}}, f.ranges())
	assert.EqualValues(t, 5, f.acks.last(f.exec.AttemptID))
}

type flakyInsert struct {
	store.Store
	failFrom int64
}

func (s *flakyInsert) InsertLogChunk(ctx context.Context, a string, c store.NewLogChunk, now time.Time) (store.LogChunk, error) {
	if s.failFrom != 0 && c.SeqFrom == s.failFrom {
		return store.LogChunk{}, errors.New("insert failed")
	}
	return s.Store.InsertLogChunk(ctx, a, c, now)
}

func TestFinish_FlushesAndReleases(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFx(t, st)
		f.mustSend(1, 0, "a", "b")
		ch, err := f.in.Subscribe(context.Background(), f.exec.ID, 0)
		require.NoError(t, err)

		require.NoError(t, f.in.Finish(context.Background(), f.exec.ID))
		assert.Equal(t, [][2]int64{{1, 2}}, f.ranges())
		assert.EqualValues(t, 2, f.acks.last(f.exec.AttemptID))
		got := drain(ch)
		assert.Equal(t, []int64{1, 2}, seqs(got), "buffered lines are delivered, then the channel closes")
		_, err = f.in.Subscribe(context.Background(), f.exec.ID, 0)
		assert.ErrorIs(t, err, ErrNoLiveTail)
		require.NoError(t, f.in.Finish(context.Background(), f.exec.ID), "finishing twice is harmless")
	})
}

func TestFinish_KeepsStateWhenFlushFails(t *testing.T) {
	f := newFx(t, openSQLite(t))
	f.mustSend(1, 0, "a")
	f.in.cfg.Store = failingStore{Store: f.st, putErr: errors.New("down")}
	require.Error(t, f.in.Finish(context.Background(), f.exec.ID))
	f.in.cfg.Store = f.st
	require.NoError(t, f.in.Finish(context.Background(), f.exec.ID))
	assert.Equal(t, [][2]int64{{1, 1}}, f.ranges())
}

func TestStop_FlushesPendingChunks(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFx(t, st)
		require.NoError(t, f.in.Start(context.Background()))
		f.mustSend(1, 0, "a", "b", "c")
		require.NoError(t, f.in.Stop(context.Background()))
		assert.Equal(t, [][2]int64{{1, 3}}, f.ranges())
		assert.EqualValues(t, 3, f.acks.last(f.exec.AttemptID))
		require.NoError(t, f.in.Stop(context.Background()), "second Stop is a no-op")
	})
}

func TestStop_HonorsContext(t *testing.T) {
	f := newFx(t, openSQLite(t))
	f.mustSend(1, 0, "a")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, f.in.Stop(ctx))
}

func TestIdleExecutionsAreEvicted(t *testing.T) {
	f := newFx(t, openSQLite(t))
	f.mustSend(1, 0, "a")
	f.flush()
	f.clock.advance(DefaultIdleTTL)
	f.in.duePass(context.Background())
	_, err := f.in.Subscribe(context.Background(), f.exec.ID, 0)
	assert.ErrorIs(t, err, ErrNoLiveTail)
	assert.EqualValues(t, 1, f.in.Stats().ExecutionsEvicted)
}

func TestStart_Twice(t *testing.T) {
	f := newFx(t, openSQLite(t))
	f.start()
	require.Error(t, f.in.Start(context.Background()))
}

func TestNew_RequiresStoreAndAcks(t *testing.T) {
	_, err := New(Config{})
	require.ErrorIs(t, err, errNoStore)
	_, err = New(Config{Store: openSQLite(t)})
	require.ErrorIs(t, err, errNoAcks)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, 5*time.Second, 5*time.Millisecond, what)
}

func drain(ch <-chan Line) []Line {
	var out []Line
	for l := range ch {
		out = append(out, l)
	}
	return out
}
