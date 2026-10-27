package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 123000000, time.UTC)

func mustPool(t *testing.T, s store.Store, name string) store.Pool {
	t.Helper()
	p, err := s.CreatePool(context.Background(), name, "desc "+name, t0)
	if err != nil {
		t.Fatalf("CreatePool %s: %v", name, err)
	}
	return p
}

func testPools(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	if _, err := s.GetPoolByName(ctx, "default"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get missing: %v; want ErrNotFound", err)
	}
	p := mustPool(t, s, "default")
	if p.ID == "" || !p.CreatedAt.Equal(t0) {
		t.Fatalf("CreatePool = %+v", p)
	}
	if _, err := s.CreatePool(ctx, "default", "dup", t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name: %v; want ErrConflict", err)
	}
	mustPool(t, s, "alpha")
	got, err := s.GetPoolByName(ctx, "default")
	if err != nil || got != p {
		t.Fatalf("GetPoolByName = %+v, %v; want %+v", got, err, p)
	}
	list, err := s.ListPools(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "alpha" || list[1].Name != "default" {
		t.Fatalf("ListPools = %+v, %v; want alpha, default", list, err)
	}
}

func testPoolTokens(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	pool := mustPool(t, s, "p1")
	plain, hash, err := tokens.New()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.CreatePoolToken(ctx, "no-such-pool", "x", hash, t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("token for missing pool: %v; want ErrNotFound", err)
	}
	tok, err := s.CreatePoolToken(ctx, pool.ID, "ci", hash, t0)
	if err != nil || tok.ID == "" || tok.PoolID != pool.ID || tok.RevokedAt != nil {
		t.Fatalf("CreatePoolToken = %+v, %v", tok, err)
	}
	if _, err := s.CreatePoolToken(ctx, pool.ID, "dup", hash, t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate hash: %v; want ErrConflict", err)
	}

	gotPool, gotTok, err := s.LookupPoolToken(ctx, hash)
	if err != nil || gotPool != pool || gotTok.ID != tok.ID || gotTok.LastUsedAt != nil {
		t.Fatalf("Lookup = %+v %+v, %v", gotPool, gotTok, err)
	}
	// The plaintext is not a valid lookup key: only the hash is stored.
	if _, _, err := s.LookupPoolToken(ctx, plain); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Lookup by plaintext: %v; want ErrNotFound", err)
	}
	if _, _, err := s.LookupPoolToken(ctx, tokens.Hash("other")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Lookup unknown: %v; want ErrNotFound", err)
	}

	used := t0.Add(time.Minute)
	if err := s.TouchPoolToken(ctx, tok.ID, used); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if err := s.TouchPoolToken(ctx, "missing", used); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Touch missing: %v; want ErrNotFound", err)
	}
	_, gotTok, _ = s.LookupPoolToken(ctx, hash)
	if gotTok.LastUsedAt == nil || !gotTok.LastUsedAt.Equal(used) {
		t.Fatalf("LastUsedAt = %v; want %v", gotTok.LastUsedAt, used)
	}

	if err := s.RevokePoolToken(ctx, tok.ID, used); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := s.RevokePoolToken(ctx, tok.ID, used); !errors.Is(err, store.ErrStale) {
		t.Fatalf("Revoke twice: %v; want ErrStale", err)
	}
	if err := s.RevokePoolToken(ctx, "missing", used); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Revoke missing: %v; want ErrNotFound", err)
	}
	if _, _, err := s.LookupPoolToken(ctx, hash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Lookup revoked: %v; want ErrNotFound", err)
	}

	second, err := s.CreatePoolToken(ctx, pool.ID, "second", tokens.Hash("two"), t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ListPoolTokens(ctx, pool.ID)
	if err != nil || len(list) != 2 || list[0].ID != tok.ID || list[1].ID != second.ID {
		t.Fatalf("ListPoolTokens = %+v, %v", list, err)
	}
	if list[0].RevokedAt == nil || !list[0].RevokedAt.Equal(used) || list[1].RevokedAt != nil {
		t.Fatalf("revoked_at wrong: %+v", list)
	}
	if other, err := s.ListPoolTokens(ctx, "other-pool"); err != nil || len(other) != 0 {
		t.Fatalf("ListPoolTokens other = %+v, %v", other, err)
	}
}

func hello(id, poolID, ctrl string) store.AgentHello {
	return store.AgentHello{
		ID: id, PoolID: poolID, Version: "1.0.0", Protocol: 1,
		Labels:       map[string]string{"zone": "a"},
		Capabilities: map[string]string{"shell": "1.0.0"},
		Capacity:     4, Telemetry: json.RawMessage(`{"cpu":1.5}`), ControllerID: ctrl, SessionSeq: 1,
	}
}

func jsonEq(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("telemetry %q: %v", got, err)
	}
	_ = json.Unmarshal([]byte(want), &w)
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("telemetry = %s; want %s", got, want)
	}
}

func testAgentLifecycle(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	pool := mustPool(t, s, "p")

	if err := s.UpsertAgentOnHello(ctx, hello("a1", "no-such-pool", "c1"), t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("hello for missing pool: %v; want ErrNotFound", err)
	}
	if _, err := s.GetAgent(ctx, "a1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get missing: %v; want ErrNotFound", err)
	}
	if err := s.UpsertAgentOnHello(ctx, hello("a1", pool.ID, "c1"), t0); err != nil {
		t.Fatalf("hello: %v", err)
	}
	a, err := s.GetAgent(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if a.PoolID != pool.ID || a.Version != "1.0.0" || a.Protocol != 1 || a.Capacity != 4 || a.ControllerID != "c1" ||
		a.Labels["zone"] != "a" || a.Capabilities["shell"] != "1.0.0" ||
		!a.ConnectedAt.Equal(t0) || !a.LastSeen.Equal(t0) || a.DisconnectedAt != nil {
		t.Fatalf("agent = %+v", a)
	}
	jsonEq(t, a.Telemetry, `{"cpu":1.5}`)

	// Nil maps and empty telemetry store as empty objects.
	bare := store.AgentHello{ID: "a-bare", PoolID: pool.ID, Version: "v", Protocol: 1, Capacity: 1, ControllerID: "c1"}
	if err := s.UpsertAgentOnHello(ctx, bare, t0); err != nil {
		t.Fatal(err)
	}
	b, err := s.GetAgent(ctx, "a-bare")
	if err != nil || len(b.Labels) != 0 || len(b.Capabilities) != 0 {
		t.Fatalf("bare agent = %+v, %v", b, err)
	}
	jsonEq(t, b.Telemetry, `{}`)

	// Disconnect: CAS on controller_id.
	t1 := t0.Add(time.Minute)
	if err := s.MarkAgentDisconnected(ctx, "a1", "c2", 1, t1); !errors.Is(err, store.ErrStale) {
		t.Fatalf("disconnect by non-owner: %v; want ErrStale", err)
	}
	if err := s.MarkAgentDisconnected(ctx, "missing", "c1", 1, t1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("disconnect missing: %v; want ErrNotFound", err)
	}
	if err := s.MarkAgentDisconnected(ctx, "a1", "c1", 1, t1); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	a, _ = s.GetAgent(ctx, "a1")
	if a.DisconnectedAt == nil || !a.DisconnectedAt.Equal(t1) || a.ControllerID != "" {
		t.Fatalf("after disconnect = %+v", a)
	}
	if err := s.MarkAgentDisconnected(ctx, "a1", "c1", 1, t1); !errors.Is(err, store.ErrStale) {
		t.Fatalf("disconnect twice: %v; want ErrStale", err)
	}

	// Reconnect to another controller; a stale controller then cannot
	// overwrite the new owner.
	t2 := t0.Add(2 * time.Minute)
	h := hello("a1", pool.ID, "c2")
	h.SessionSeq = 2
	h.Capacity = 8
	if err := s.UpsertAgentOnHello(ctx, h, t2); err != nil {
		t.Fatal(err)
	}
	a, _ = s.GetAgent(ctx, "a1")
	if a.ControllerID != "c2" || a.Capacity != 8 || a.DisconnectedAt != nil || !a.ConnectedAt.Equal(t2) {
		t.Fatalf("after reconnect = %+v", a)
	}
	if err := s.MarkAgentDisconnected(ctx, "a1", "c1", 1, t2.Add(time.Second)); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale controller disconnect: %v; want ErrStale", err)
	}
	if a, _ = s.GetAgent(ctx, "a1"); a.ControllerID != "c2" || a.DisconnectedAt != nil {
		t.Fatalf("stale controller changed ownership: %+v", a)
	}
}

func testBatchTouchAgents(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	pool := mustPool(t, s, "p")
	for _, h := range []store.AgentHello{hello("a1", pool.ID, "c1"), hello("a2", pool.ID, "c1"), hello("a3", pool.ID, "c2")} {
		if err := s.UpsertAgentOnHello(ctx, h, t0); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.BatchTouchAgents(ctx, "c1", nil); err != nil || n != 0 {
		t.Fatalf("empty batch = %d, %v", n, err)
	}
	t1 := t0.Add(10 * time.Second)
	n, err := s.BatchTouchAgents(ctx, "c1", []store.AgentTouch{
		{ID: "a1", SessionSeq: 1, LastSeen: t1, Telemetry: json.RawMessage(`{"cpu":9}`)},
		{ID: "a2", SessionSeq: 1, LastSeen: t1},
		{ID: "a3", SessionSeq: 1, LastSeen: t1, Telemetry: json.RawMessage(`{"cpu":99}`)}, // owned by c2: skipped
		{ID: "ghost", SessionSeq: 1, LastSeen: t1},
	})
	if err != nil || n != 2 {
		t.Fatalf("BatchTouchAgents = %d, %v; want 2", n, err)
	}
	a1, _ := s.GetAgent(ctx, "a1")
	if !a1.LastSeen.Equal(t1) {
		t.Fatalf("a1.LastSeen = %v; want %v", a1.LastSeen, t1)
	}
	jsonEq(t, a1.Telemetry, `{"cpu":9}`)
	a3, _ := s.GetAgent(ctx, "a3")
	if !a3.LastSeen.Equal(t0) {
		t.Fatalf("non-owner touch applied: %+v", a3)
	}
	jsonEq(t, a3.Telemetry, `{"cpu":1.5}`)

	// After disconnect no controller owns the agent, so touches do not apply.
	if err := s.MarkAgentDisconnected(ctx, "a1", "c1", 1, t1); err != nil {
		t.Fatal(err)
	}
	if n, err := s.BatchTouchAgents(ctx, "c1", []store.AgentTouch{{ID: "a1", SessionSeq: 1, LastSeen: t1.Add(time.Second)}}); err != nil || n != 0 {
		t.Fatalf("touch after disconnect = %d, %v; want 0", n, err)
	}
}

func testListAgents(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	pool := mustPool(t, s, "p")
	if page, err := s.ListAgents(ctx, "", 10); err != nil || len(page.Agents) != 0 || page.NextCursor != "" {
		t.Fatalf("empty list = %+v, %v", page, err)
	}
	for i := range 5 {
		if err := s.UpsertAgentOnHello(ctx, hello(fmt.Sprintf("agent-%d", i), pool.ID, "c1"), t0); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("pagination did not terminate")
		}
		page, err := s.ListAgents(ctx, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range page.Agents {
			seen = append(seen, a.ID)
		}
		if page.NextCursor == "" {
			if pages != 2 || len(page.Agents) != 1 {
				t.Fatalf("last page %d has %d agents", pages, len(page.Agents))
			}
			break
		}
		cursor = page.NextCursor
	}
	if want := "agent-0,agent-1,agent-2,agent-3,agent-4"; strings.Join(seen, ",") != want {
		t.Fatalf("seen = %v; want %s", seen, want)
	}
	// An exact multiple of the page size ends with an empty cursor.
	if page, err := s.ListAgents(ctx, "", 5); err != nil || len(page.Agents) != 5 || page.NextCursor != "" {
		t.Fatalf("exact page = %d agents, cursor %q, %v", len(page.Agents), page.NextCursor, err)
	}
	if page, err := s.ListAgents(ctx, "", 0); err != nil || len(page.Agents) != 5 {
		t.Fatalf("default limit = %d agents, %v", len(page.Agents), err)
	}
}

func testControllerInstances(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	if err := s.HeartbeatControllerInstance(ctx, "c1", t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("heartbeat missing: %v; want ErrNotFound", err)
	}
	if err := s.DeleteControllerInstance(ctx, "c1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete missing: %v; want ErrNotFound", err)
	}
	if err := s.RegisterControllerInstance(ctx, "c2", "http://c2:9000", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterControllerInstance(ctx, "c1", "http://c1:9000", t0); err != nil {
		t.Fatal(err)
	}
	t1 := t0.Add(5 * time.Second)
	if err := s.HeartbeatControllerInstance(ctx, "c1", t1); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListControllerInstances(ctx)
	if err != nil || len(list) != 2 || list[0].ID != "c1" || list[1].ID != "c2" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if list[0].InternalURL != "http://c1:9000" || !list[0].StartedAt.Equal(t0) || !list[0].LastSeen.Equal(t1) {
		t.Fatalf("c1 = %+v", list[0])
	}
	// Re-registering (restart with the same id) resets the row.
	t2 := t0.Add(time.Hour)
	if err := s.RegisterControllerInstance(ctx, "c1", "http://c1:9001", t2); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ListControllerInstances(ctx)
	if list[0].InternalURL != "http://c1:9001" || !list[0].StartedAt.Equal(t2) || !list[0].LastSeen.Equal(t2) {
		t.Fatalf("re-registered c1 = %+v", list[0])
	}
	if err := s.DeleteControllerInstance(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.ListControllerInstances(ctx); len(list) != 1 || list[0].ID != "c2" {
		t.Fatalf("after delete = %+v", list)
	}
}

func ownerIs(t *testing.T, s store.Store, id, ctrl string, seq uint64) {
	t.Helper()
	a, err := s.GetAgent(context.Background(), id)
	if err != nil || a.ControllerID != ctrl || a.SessionSeq != seq {
		t.Fatalf("agent %s = %+v, %v; want owner %s seq %d", id, a, err, ctrl, seq)
	}
}

func testAgentSessionOrdering(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	pool := mustPool(t, s, "p")
	other := mustPool(t, s, "q")

	// B (seq 2) wins; a delayed hello from A (seq 1) arriving later, even
	// with a later wall clock, is stale and changes nothing.
	hb := hello("a1", pool.ID, "B")
	hb.SessionSeq = 2
	if err := s.UpsertAgentOnHello(ctx, hb, t0); err != nil {
		t.Fatal(err)
	}
	ha := hello("a1", pool.ID, "A")
	ha.SessionSeq = 1
	if err := s.UpsertAgentOnHello(ctx, ha, t0.Add(time.Hour)); !errors.Is(err, store.ErrStale) {
		t.Fatalf("delayed hello: %v; want ErrStale", err)
	}
	ownerIs(t, s, "a1", "B", 2)
	if err := s.UpsertAgentOnHello(ctx, hb, t0.Add(time.Hour)); !errors.Is(err, store.ErrStale) {
		t.Fatalf("same-seq hello: %v; want ErrStale", err)
	}
	// A's disconnect for its stale session changes nothing.
	if err := s.MarkAgentDisconnected(ctx, "a1", "A", 1, t0.Add(time.Minute)); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale disconnect: %v; want ErrStale", err)
	}
	// Right controller, wrong session is also stale.
	if err := s.MarkAgentDisconnected(ctx, "a1", "B", 1, t0.Add(time.Minute)); !errors.Is(err, store.ErrStale) {
		t.Fatalf("wrong-session disconnect: %v; want ErrStale", err)
	}
	if a, _ := s.GetAgent(ctx, "a1"); a.DisconnectedAt != nil {
		t.Fatalf("stale disconnect changed the row: %+v", a)
	}

	// B takes over from A; A's touch is then skipped.
	if err := s.UpsertAgentOnHello(ctx, hello("a2", pool.ID, "A"), t0); err != nil {
		t.Fatal(err)
	}
	h2 := hello("a2", pool.ID, "B")
	h2.SessionSeq = 2
	if err := s.UpsertAgentOnHello(ctx, h2, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	t1 := t0.Add(time.Minute)
	n, err := s.BatchTouchAgents(ctx, "A", []store.AgentTouch{{ID: "a2", SessionSeq: 1, LastSeen: t1}})
	if err != nil || n != 0 {
		t.Fatalf("touch by previous owner = %d, %v; want 0", n, err)
	}
	if n, err = s.BatchTouchAgents(ctx, "B", []store.AgentTouch{{ID: "a2", SessionSeq: 1, LastSeen: t1}}); err != nil || n != 0 {
		t.Fatalf("touch with old session = %d, %v; want 0", n, err)
	}
	if n, err = s.BatchTouchAgents(ctx, "B", []store.AgentTouch{{ID: "a2", SessionSeq: 2, LastSeen: t1}}); err != nil || n != 1 {
		t.Fatalf("touch by owner = %d, %v; want 1", n, err)
	}

	// Pool mismatch is a conflict, not a takeover.
	hp := hello("a1", other.ID, "C")
	hp.SessionSeq = 9
	if err := s.UpsertAgentOnHello(ctx, hp, t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("pool mismatch: %v; want ErrConflict", err)
	}
	ownerIs(t, s, "a1", "B", 2)
}

func testTouchLastSeenMonotonic(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	pool := mustPool(t, s, "p")
	if err := s.UpsertAgentOnHello(ctx, hello("a1", pool.ID, "c1"), t0); err != nil {
		t.Fatal(err)
	}
	t1, t2 := t0.Add(20*time.Second), t0.Add(10*time.Second)
	if n, err := s.BatchTouchAgents(ctx, "c1", []store.AgentTouch{{ID: "a1", SessionSeq: 1, LastSeen: t1, Telemetry: json.RawMessage(`{"cpu":2}`)}}); err != nil || n != 1 {
		t.Fatalf("touch = %d, %v", n, err)
	}
	// A late batch (older last_seen) is skipped, not an error, and keeps the newer telemetry.
	n, err := s.BatchTouchAgents(ctx, "c1", []store.AgentTouch{{ID: "a1", SessionSeq: 1, LastSeen: t2, Telemetry: json.RawMessage(`{"cpu":1}`)}})
	if err != nil || n != 0 {
		t.Fatalf("late touch = %d, %v; want 0, nil", n, err)
	}
	a, _ := s.GetAgent(ctx, "a1")
	if !a.LastSeen.Equal(t1) {
		t.Fatalf("LastSeen = %v; want %v", a.LastSeen, t1)
	}
	jsonEq(t, a.Telemetry, `{"cpu":2}`)
	// An equal last_seen still applies.
	if n, err = s.BatchTouchAgents(ctx, "c1", []store.AgentTouch{{ID: "a1", SessionSeq: 1, LastSeen: t1}}); err != nil || n != 1 {
		t.Fatalf("equal touch = %d, %v; want 1", n, err)
	}
}

func testHelloValidation(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	pool := mustPool(t, s, "p")
	tests := []struct{ name, pool, ctrl string }{
		{"empty controller", pool.ID, ""},
		{"empty pool", "", "c1"},
	}
	for _, tt := range tests {
		err := s.UpsertAgentOnHello(ctx, hello("a1", tt.pool, tt.ctrl), t0)
		if !errors.Is(err, store.ErrInvalid) || errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s: %v; want ErrInvalid", tt.name, err)
		}
	}
	if _, err := s.GetAgent(ctx, "a1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("invalid hello created a row: %v", err)
	}
}

func testTimestampOrdering(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	pool := mustPool(t, s, "p")
	base := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	// Created out of order, with sub-second gaps and sub-microsecond noise.
	times := []time.Time{base.Add(time.Second), base, base.Add(500*time.Millisecond + 123*time.Nanosecond)}
	ids := make([]string, len(times))
	for i, ts := range times {
		tok, err := s.CreatePoolToken(ctx, pool.ID, fmt.Sprintf("t%d", i), tokens.Hash(fmt.Sprint(i)), ts)
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = tok.ID
		if !tok.CreatedAt.Equal(store.NormalizeTime(ts)) {
			t.Fatalf("CreatedAt = %v; want %v", tok.CreatedAt, store.NormalizeTime(ts))
		}
	}
	list, err := s.ListPoolTokens(ctx, pool.ID)
	if err != nil || len(list) != 3 || list[0].ID != ids[1] || list[1].ID != ids[2] || list[2].ID != ids[0] {
		t.Fatalf("ListPoolTokens order = %+v, %v; want t, t+500ms, t+1s", list, err)
	}
	// Returned and re-read values agree exactly.
	if !list[1].CreatedAt.Equal(store.NormalizeTime(times[2])) {
		t.Fatalf("round trip = %v; want %v", list[1].CreatedAt, store.NormalizeTime(times[2]))
	}
}
