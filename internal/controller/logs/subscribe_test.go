package logs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func recv(t *testing.T, ch <-chan Line) Line {
	t.Helper()
	select {
	case l, ok := <-ch:
		require.True(t, ok, "channel closed")
		return l
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a line")
		return Line{}
	}
}

func TestRing_OverflowKeepsNewest(t *testing.T) {
	f := newFx(t, openSQLite(t))
	f.mustSend(1, 0, numbered(1, 6000)...)

	ch, err := f.in.Subscribe(context.Background(), f.exec.ID, 0)
	require.NoError(t, err)
	var got []Line
	for range DefaultRingLines {
		got = append(got, recv(t, ch))
	}
	assert.EqualValues(t, 1001, got[0].Seq, "the oldest 1000 lines fell out of the ring")
	assert.EqualValues(t, 6000, got[len(got)-1].Seq)
	assert.Equal(t, "line 6000", got[len(got)-1].Line)

	// the ring is only the live tail: everything is still persisted
	f.flush()
	assert.Len(t, f.stored(), 6000)
}

func TestSubscribe_ReplaysAfterSeqThenLive(t *testing.T) {
	tests := []struct {
		name     string
		after    int64
		wantFrom int64
	}{
		{"from the start", 0, 1},
		{"middle", 3, 4},
		{"past the end replays nothing", 5, 6},
		{"before the ring replays all of it", -10, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t, openSQLite(t))
			f.mustSend(1, 0, numbered(1, 5)...)
			ch, err := f.in.Subscribe(context.Background(), f.exec.ID, tc.after)
			require.NoError(t, err)

			f.mustSend(6, 0, "live-6", "live-7")
			for want := tc.wantFrom; want <= 7; want++ {
				assert.Equal(t, want, recv(t, ch).Seq)
			}
		})
	}
}

func TestSubscribe_NoBufferWithoutIngest(t *testing.T) {
	f := newFx(t, openSQLite(t))
	_, err := f.in.Subscribe(context.Background(), f.exec.ID, 0)
	assert.ErrorIs(t, err, ErrNoLiveTail)
}

func TestSubscribe_ContextCancelClosesChannel(t *testing.T) {
	f := newFx(t, openSQLite(t))
	f.mustSend(1, 0, "a")
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := f.in.Subscribe(ctx, f.exec.ID, 0)
	require.NoError(t, err)
	recv(t, ch)
	cancel()
	require.Eventually(t, func() bool {
		select {
		case _, ok := <-ch:
			return !ok
		default:
			return false
		}
	}, 5*time.Second, time.Millisecond)
	f.mustSend(2, 0, "b") // ingest keeps working with the subscriber gone
}

func TestSubscribe_SlowSubscriberIsDroppedAndNeverBlocksIngest(t *testing.T) {
	f := newFx(t, openSQLite(t), func(c *Config) { c.SubscriberQueue = 4 })
	f.mustSend(1, 0, "a")
	slow, err := f.in.Subscribe(context.Background(), f.exec.ID, 0)
	require.NoError(t, err)
	fast, err := f.in.Subscribe(context.Background(), f.exec.ID, 0)
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 2; i <= 30; i++ {
			f.mustSend(uint64(i), 0, "x")
			recv(t, fast) // the fast subscriber keeps up
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ingest blocked on a slow subscriber")
	}

	// The slow one got its buffered lines, then its channel was closed.
	n := len(drain(slow))
	assert.Greater(t, n, 0)
	assert.Less(t, n, 30)
	assert.EqualValues(t, 1, f.in.Stats().SubscribersDropped)
}

func TestSubscribe_StopClosesSubscribers(t *testing.T) {
	f := newFx(t, openSQLite(t))
	f.mustSend(1, 0, "a")
	ch, err := f.in.Subscribe(context.Background(), f.exec.ID, 0)
	require.NoError(t, err)
	require.NoError(t, f.in.Stop(context.Background()))
	assert.Len(t, drain(ch), 1)
}
