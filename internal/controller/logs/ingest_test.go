package logs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
)

type batch struct {
	from, gapTo uint64
	lines       []string
}

func TestAppend_OrderingDuplicatesAndGaps(t *testing.T) {
	tests := []struct {
		name       string
		batches    []batch
		wantSeqs   []int64
		wantRanges [][2]int64
		wantAck    uint64
	}{
		{
			name:       "in order",
			batches:    []batch{{1, 0, []string{"a", "b"}}, {3, 0, []string{"c"}}},
			wantSeqs:   []int64{1, 2, 3},
			wantRanges: [][2]int64{{1, 3}},
			wantAck:    3,
		},
		{
			name:       "resent overlap is dropped",
			batches:    []batch{{1, 0, []string{"a", "b", "c"}}, {2, 0, []string{"b", "c", "d"}}},
			wantSeqs:   []int64{1, 2, 3, 4},
			wantRanges: [][2]int64{{1, 4}},
			wantAck:    4,
		},
		{
			name:       "full duplicate is a no-op",
			batches:    []batch{{1, 0, []string{"a", "b"}}, {1, 0, []string{"a", "b"}}},
			wantSeqs:   []int64{1, 2},
			wantRanges: [][2]int64{{1, 2}},
			wantAck:    2,
		},
		{
			name:       "declared gap is contiguous",
			batches:    []batch{{1, 0, []string{"a", "b"}}, {3, 5, []string{"f", "g"}}},
			wantSeqs:   []int64{1, 2, 6, 7},
			wantRanges: [][2]int64{{1, 2}, {6, 7}},
			wantAck:    7,
		},
		{
			name:       "gap with no entries advances the ack",
			batches:    []batch{{1, 0, []string{"a"}}, {2, 9, nil}},
			wantSeqs:   []int64{1},
			wantRanges: [][2]int64{{1, 1}},
			wantAck:    9,
		},
		{
			name:       "gap at the start",
			batches:    []batch{{1, 4, []string{"e"}}},
			wantSeqs:   []int64{5},
			wantRanges: [][2]int64{{5, 5}},
			wantAck:    5,
		},
		{
			name:       "undeclared hole is treated as a gap",
			batches:    []batch{{1, 0, []string{"a"}}, {4, 0, []string{"d"}}},
			wantSeqs:   []int64{1, 4},
			wantRanges: [][2]int64{{1, 1}, {4, 4}},
			wantAck:    4,
		},
		{
			name:       "gap already covered by ingested lines",
			batches:    []batch{{1, 0, []string{"a", "b", "c"}}, {2, 3, []string{"d"}}},
			wantSeqs:   []int64{1, 2, 3, 4},
			wantRanges: [][2]int64{{1, 4}},
			wantAck:    4,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			forEachBackend(t, func(t *testing.T, st store.Store) {
				f := newFx(t, st)
				for _, b := range tc.batches {
					f.mustSend(b.from, b.gapTo, b.lines...)
				}
				f.flush()
				assert.Equal(t, tc.wantRanges, f.ranges())
				assert.Equal(t, tc.wantSeqs, seqs(f.stored()))
				assert.Equal(t, tc.wantAck, f.acks.last(f.exec.AttemptID))
			})
		})
	}
}

func TestAppend_StoredLinesRoundTrip(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFx(t, st)
		f.mustSend(1, 0, "alpha", "beta <&> \"q\"")
		f.flush()
		got := f.stored()
		require.Len(t, got, 2)
		assert.Equal(t, Line{Seq: 1, TS: t0, Stream: StreamOut, Line: "alpha"}, got[0])
		assert.Equal(t, "beta <&> \"q\"", got[1].Line)
		assert.Equal(t, t0.Add(1), got[1].TS)
		c := f.chunks()[0]
		assert.Equal(t, "db", c.Backend)
		assert.Equal(t, "logs/"+f.exec.ID+"/1-2.ndjson", c.ObjectKey)
		assert.WithinDuration(t, t0, c.TsFirst, time.Second)
		assert.WithinDuration(t, t0, c.TsLast, time.Second)
	})
}

func TestAppend_InvalidParams(t *testing.T) {
	f := newFx(t, openSQLite(t))
	tests := []struct {
		name string
		m    protocol.LogAppend
	}{
		{"no attempt", protocol.LogAppend{SeqFrom: 1, Entries: entries("a")}},
		{"seq_from zero", protocol.LogAppend{AttemptID: f.exec.AttemptID, SeqFrom: 0, Entries: entries("a")}},
		{"absurd seq", protocol.LogAppend{AttemptID: f.exec.AttemptID, SeqFrom: 1 << 63, Entries: entries("a")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := f.in.Append(context.Background(), "agent-1", tc.m)
			var rpc *protocol.RPCError
			require.ErrorAs(t, err, &rpc)
			assert.Equal(t, protocol.CodeInvalidParams, rpc.Code)
		})
	}
	assert.Zero(t, f.in.Stats().LinesIngested)
}

func TestFencing_StaleAttemptWritesNothing(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFx(t, st)
		require.NoError(t, f.sendAs("no-such-attempt", 1, 0, "x", "y"))

		_, err := f.in.Subscribe(context.Background(), f.exec.ID, 0)
		require.ErrorIs(t, err, ErrNoLiveTail, "no ring may exist for a stale attempt")
		require.NoError(t, f.in.Flush(context.Background(), f.exec.ID))
		assert.Empty(t, f.chunks())
		assert.EqualValues(t, 1, f.in.Stats().StaleBatches)
		assert.Zero(t, f.in.Stats().LinesIngested)

		// the agent is told to free the batch, once the pass runs
		f.in.sendAcks(context.Background())
		assert.EqualValues(t, 2, f.acks.last("no-such-attempt"))
	})
}

func TestFencing_SupersededAttempt(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFx(t, st)
		oldAttempt := f.exec.AttemptID
		f.mustSend(1, 0, "old-1", "old-2")
		fresh := f.reclaim()
		require.NotEqual(t, oldAttempt, fresh.AttemptID)
		require.Equal(t, f.exec.ID, fresh.ID)

		// The new attempt takes over the buffer; the old one is re-verified
		// against the store even though its lookup is still cached.
		require.NoError(t, f.in.Append(context.Background(), "agent-2", protocol.LogAppend{
			AttemptID: fresh.AttemptID, SeqFrom: 1, Entries: entries("new-1"),
		}))
		before := f.in.Stats().LinesIngested
		require.NoError(t, f.sendAs(oldAttempt, 3, 0, "late"))
		assert.Equal(t, before, f.in.Stats().LinesIngested, "stale attempt must not feed the ring")

		require.NoError(t, f.in.Flush(context.Background(), f.exec.ID))
		lines := f.stored()
		require.Len(t, lines, 1)
		assert.Equal(t, "new-1", lines[0].Line)
		assert.EqualValues(t, 1, f.in.Stats().StaleBatches)
	})
}

func TestFencing_CachedAttemptIsOnlyTrustedForTTL(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFx(t, st)
		f.mustSend(1, 0, "before") // caches the attempt
		require.NoError(t, f.in.Finish(context.Background(), f.exec.ID))
		f.reclaim()

		// Within the TTL the cached lookup may let the batch into the buffer,
		// but the fenced chunk insert still refuses to persist it.
		f.mustSend(2, 0, "within-ttl")
		require.NoError(t, f.in.Flush(context.Background(), f.exec.ID))
		assert.Equal(t, [][2]int64{{1, 1}}, f.ranges(), "stale lines must never reach the manifest")
		_, err := f.in.Subscribe(context.Background(), f.exec.ID, 0)
		require.ErrorIs(t, err, ErrNoLiveTail, "the stale buffer is discarded")

		// Past the TTL the attempt is looked up again: no buffer at all.
		f.clock.advance(DefaultAttemptTTL + time.Second)
		before := f.in.Stats().LinesIngested
		f.mustSend(3, 0, "after-ttl")
		assert.Equal(t, before, f.in.Stats().LinesIngested)
		_, err = f.in.Subscribe(context.Background(), f.exec.ID, 0)
		require.ErrorIs(t, err, ErrNoLiveTail)
	})
}

func TestFencing_StoreLookupErrorIsReported(t *testing.T) {
	f := newFx(t, openSQLite(t))
	boom := errors.New("db down")
	f.in.cfg.Store = failingStore{Store: f.st, getErr: boom}
	err := f.send(1, 0, "a")
	require.ErrorIs(t, err, boom)
	assert.Zero(t, f.in.Stats().StaleBatches, "a transient error is not a fencing verdict")
	assert.Empty(t, f.acks.all())
}

func TestAckIsCoalescedPerAttempt(t *testing.T) {
	f := newFx(t, openSQLite(t))
	f.mustSend(1, 0, "a", "b", "c")
	f.flush()
	base := len(f.acks.all())
	for range 20 { // the agent keeps resending the same unacked batch
		f.mustSend(1, 0, "a", "b", "c")
	}
	f.in.sendAcks(context.Background())
	assert.Equal(t, base+1, len(f.acks.all()), "20 duplicates must produce one ack")
	assert.EqualValues(t, 3, f.acks.last(f.exec.AttemptID))
}

func TestAck_FailureToSendIsIgnored(t *testing.T) {
	f := newFx(t, openSQLite(t))
	f.acks.err = errors.New("agent gone")
	f.mustSend(1, 0, "a")
	f.flush()
	assert.Empty(t, f.acks.all())
	assert.Zero(t, f.in.Stats().AcksSent)
	// the resend after reconnect gets a fresh ack
	f.acks.err = nil
	f.mustSend(1, 0, "a")
	f.in.sendAcks(context.Background())
	assert.EqualValues(t, 1, f.acks.last(f.exec.AttemptID))
}

func TestLogsNeverContainLineText(t *testing.T) {
	f := newFx(t, openSQLite(t))
	const secretish = "TOP-SECRET-LINE-CONTENT"
	f.mustSend(1, 0, secretish)
	require.NoError(t, f.sendAs("unknown", 1, 0, secretish))
	f.flush()
	f.in.cfg.Store = failingStore{Store: f.st, putErr: errors.New("disk full")}
	f.mustSend(2, 0, secretish)
	require.Error(t, f.in.Flush(context.Background(), f.exec.ID))
	require.NoError(t, f.in.Finish(context.Background(), "nope"))

	out := f.logs.String()
	assert.NotEmpty(t, out, "the test must exercise logging")
	assert.False(t, strings.Contains(out, secretish), "controller logs must never contain log line text")
}
