//go:build chaos

package chaos

import (
	"context"
	"testing"
	"time"
)

// leaderLease reads the reaper lease row: holder, term and whether it is still
// valid (the database clock is close enough to the controllers' in tests).
func (c *Cluster) leaderLease() (holder string, term int64, valid bool) {
	c.t.Helper()
	err := c.read.Pool().QueryRow(context.Background(),
		"SELECT holder, term, expires_at > now() FROM leader_leases WHERE name = 'reaper'").Scan(&holder, &term, &valid)
	if err != nil {
		return "", 0, false
	}
	return holder, term, valid
}

// B4 (#88): the leader controller dies without releasing. Another controller
// takes the reaper lease with the next term within the ttl plus a renewal.
func TestLeaderFailoverAfterControllerCrash(t *testing.T) {
	c := newEmptyCluster(t)
	c.addController(timing, nil)
	eventually(t, "A holds the reaper lease", func() bool { h, term, v := c.leaderLease(); return v && h == "ctl-A" && term == 1 })
	c.addController(timing, nil)

	// A's connections go away: it can neither renew nor release.
	if err := c.ctls[0].st.Close(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	eventually(t, "B takes over with term 2", func() bool { h, term, v := c.leaderLease(); return v && h == "ctl-B" && term == 2 })
	if took := time.Since(start); took > 2*timing.LeaderTTL+timing.LeaderRenew+2*time.Second {
		t.Fatalf("failover took %s", took)
	}
}

// A graceful stop releases the lease, so the other controller takes over long
// before the ttl would have run out.
func TestLeaderHandoverOnGracefulStop(t *testing.T) {
	c := newCluster(t, 2)
	eventually(t, "a controller leads", func() bool { _, _, v := c.leaderLease(); return v })
	holder, term, _ := c.leaderLease()
	var leader *controllerNode
	for _, cn := range c.ctls {
		if cn.id == holder {
			leader = cn
		}
	}
	if leader == nil {
		t.Fatalf("holder %q is not a controller", holder)
	}
	start := time.Now()
	if err := leader.stop(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the other controller leads", func() bool {
		h, tm, v := c.leaderLease()
		return v && h != holder && tm == term+1
	})
	if took := time.Since(start); took >= timing.LeaderTTL {
		t.Fatalf("handover took %s; want less than the ttl %s", took, timing.LeaderTTL)
	}
}
