package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

// stabilityClock fires every timer at once except the stability timer, whose
// duration is the heartbeat interval the controller negotiated; stable decides
// whether that one fires. Waits are recorded.
type stabilityClock struct {
	mu       sync.Mutex
	ds       []time.Duration
	interval time.Duration
	stable   bool
}

func (s *stabilityClock) clock() Clock {
	return Clock{NewTimer: func(d time.Duration) Timer {
		c := make(chan time.Time, 1)
		if d == s.interval {
			if s.stable {
				c <- time.Time{}
			}
			return fakeTimer{c}
		}
		s.mu.Lock()
		s.ds = append(s.ds, d)
		s.mu.Unlock()
		c <- time.Time{}
		return fakeTimer{c}
	}}
}

func (s *stabilityClock) delays() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.ds...)
}

// TestBackoffResetsOnlyAfterStableSession drops every connection right after
// hello. Without a stable period the waits keep doubling; once sessions
// outlive one heartbeat interval each wait restarts at the first step.
func TestBackoffResetsOnlyAfterStableSession(t *testing.T) {
	const interval = 777 * time.Millisecond
	tests := []struct {
		name   string
		stable bool
		want   []time.Duration
	}{
		{"flapping session keeps growing", false, []time.Duration{1, 2, 4, 8}},
		{"stable session resets", true, []time.Duration{1, 1, 1, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			forEachBackend(t, func(t *testing.T, st store.Store) {
				f := newFixture(t, st)
				f.ctl.restartWith(interval)
				sc := &stabilityClock{interval: interval, stable: tt.stable}
				f.agent("agent-1", func(c *Config) {
					c.Clock, c.Rand = sc.clock(), func() float64 { return 1 }
					c.BackoffBase, c.BackoffMax = DefaultBackoffBase, DefaultBackoffMax
				})
				stop := make(chan struct{})
				done := make(chan struct{})
				go func() { // drop each session as soon as it is established
					defer close(done)
					for {
						select {
						case <-stop:
							return
						case <-time.After(time.Millisecond):
						}
						if p, ok := f.ctl.gw.Peer("agent-1"); ok {
							_ = p.Close()
						}
					}
				}()
				defer func() { close(stop); <-done }()
				eventually(t, "four waits", func() bool { return len(sc.delays()) >= 4 })
				got := sc.delays()
				for i, w := range tt.want {
					if got[i] != w*time.Second {
						t.Fatalf("waits = %v, want prefix %v seconds", got[:4], tt.want)
					}
				}
			})
		})
	}
}
