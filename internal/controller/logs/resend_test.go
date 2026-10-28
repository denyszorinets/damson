package logs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/store"
)

// A batch that skips seqs without declaring a gap is refused and never acked:
// acking past it would let the agent free lines the controller never saw.
func TestAppend_UndeclaredHoleIsRefusedAndNeverAcked(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFx(t, st)
		f.mustSend(1, 0, "a", "b")
		err := f.send(5, 0, "e")
		require.ErrorIs(t, err, ErrNeedResend)
		f.flush()
		assert.Equal(t, [][2]int64{{1, 2}}, f.ranges(), "the batch beyond the hole was not kept")
		assert.EqualValues(t, 2, f.acks.last(f.exec.AttemptID), "ack stays at the contiguous frontier")

		// The agent reconnects and resends everything after its last ack.
		f.mustSend(3, 0, "c", "d", "e")
		f.flush()
		assert.Equal(t, [][2]int64{{1, 2}, {3, 5}}, f.ranges())
		assert.EqualValues(t, 5, f.acks.last(f.exec.AttemptID))
		assert.EqualValues(t, 1, f.in.Stats().BatchesOutOfOrder)
	})
}

// A state created again for the same attempt starts at what was acknowledged,
// not at whatever batch arrives first.
func TestAppend_FloorSurvivesStateRecreation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFx(t, st)
		f.mustSend(1, 0, "a", "b", "c")
		require.NoError(t, f.in.Finish(context.Background(), f.exec.ID))
		assert.EqualValues(t, 3, f.acks.last(f.exec.AttemptID))

		require.ErrorIs(t, f.send(7, 0, "g"), ErrNeedResend, "lines 4-6 were never ingested")
		f.mustSend(4, 0, "d")
		f.flush()
		assert.EqualValues(t, 4, f.acks.last(f.exec.AttemptID))
	})
}

// A transient store error while resolving the attempt asks for a resend after
// a few retries instead of dropping the batch silently.
func TestAppend_TransientLookupErrorAsksForResend(t *testing.T) {
	f := newFx(t, openSQLite(t), func(c *Config) { c.LookupRetries, c.LookupRetryWait = 2, time.Millisecond })
	f.mustSend(1, 0, "a")
	f.in.cache.forget(f.exec.AttemptID)
	f.in.cfg.Store = failingStore{Store: f.st, getErr: errors.New("db timeout")}
	require.ErrorIs(t, f.send(2, 0, "b"), ErrNeedResend)
	assert.EqualValues(t, 1, f.in.Stats().BatchesRefused)

	f.in.cfg.Store = f.st // the database is back; the agent resends
	f.mustSend(2, 0, "b")
	f.flush()
	assert.EqualValues(t, 2, f.acks.last(f.exec.AttemptID))
}

// Past the pending cap a batch is refused (and the agent made to resend)
// instead of letting memory grow while the store is down.
func TestAppend_PendingCapRefusesBatches(t *testing.T) {
	f := newFx(t, openSQLite(t), func(c *Config) { c.MaxPendingBytes = 200 })
	f.mustSend(1, 0, numbered(1, 10)...)
	require.ErrorIs(t, f.send(11, 0, "more"), ErrNeedResend)
	assert.EqualValues(t, 1, f.in.Stats().BatchesRefused)
	f.flush() // the store catches up
	f.mustSend(11, 0, "more")
}

// A late batch of a superseded attempt must neither replace the current
// attempt's buffer nor be kept: the store decides which attempt is current.
func TestRoute_SupersededAttemptCannotReplaceCurrentState(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFx(t, st)
		old := f.exec.AttemptID
		f.mustSend(1, 0, "a1-line")
		cur := f.reclaim()
		f.exec = cur
		f.mustSend(1, 0, "a2-line-1", "a2-line-2") // the new attempt takes over the buffer

		f.in.cache.put(old, f.exec.ID, f.clock.now) // a stale cached answer says old is fine
		require.NoError(t, f.sendAs(old, 1, 0, "stale-1", "stale-2", "stale-3"))

		f.flush() // also sends the queued acks
		assert.EqualValues(t, 3, f.acks.last(old), "the superseded attempt is acknowledged so the agent frees it")
		var got []string
		for _, l := range f.stored() {
			got = append(got, l.Line)
		}
		assert.Equal(t, []string{"a2-line-1", "a2-line-2"}, got, "only the current attempt's lines are stored")
	})
}

// A closed session is not needed to test Register's contract: Append reports
// ErrNeedResend, which Register turns into closing the peer. Here the handler
// path is checked for the stopped case, which must not create state.
func TestAppend_AfterStopIsRejected(t *testing.T) {
	f := newFx(t, openSQLite(t))
	f.start()
	require.NoError(t, f.in.Stop(context.Background()))
	err := f.send(1, 0, "a")
	require.ErrorIs(t, err, ErrStopped)
	assert.Nil(t, f.in.state(f.exec.ID), "no state is created after Stop")
}

// Idle eviction keeps the buffer of a quiet execution that still has a live
// tail subscriber.
func TestEviction_SkipsExecutionsWithSubscribers(t *testing.T) {
	f := newFx(t, openSQLite(t))
	f.mustSend(1, 0, "a")
	f.flush()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := f.in.Subscribe(ctx, f.exec.ID, 0)
	require.NoError(t, err)
	<-ch
	f.clock.advance(DefaultIdleTTL + time.Minute)
	f.in.duePass(context.Background())
	assert.NotNil(t, f.in.state(f.exec.ID), "a subscribed execution is not evicted")
	cancel()
	require.Eventually(t, func() bool {
		f.in.duePass(context.Background())
		return f.in.state(f.exec.ID) == nil
	}, 5*time.Second, 10*time.Millisecond, "evicted once nobody is listening")
}
