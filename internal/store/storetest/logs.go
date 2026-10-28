package storetest

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

func chunk(execID string, from, to int64, key string) store.NewLogChunk {
	return store.NewLogChunk{
		ExecutionID: execID, SeqFrom: from, SeqTo: to, Backend: "db", ObjectKey: key, Bytes: 100,
		TsFirst: t0.Add(time.Duration(from) * time.Second), TsLast: t0.Add(time.Duration(to) * time.Second),
	}
}

func testLogChunks(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	e := claimedOne(t, s)

	c1, err := s.InsertLogChunk(ctx, e.AttemptID, chunk(e.ID, 1, 10, "logs/r/e/1-10"), claimNow)
	if err != nil || c1.ID == "" || c1.Backend != "db" || !c1.CreatedAt.Equal(claimNow) || !c1.TsFirst.Equal(t0.Add(time.Second)) {
		t.Fatalf("InsertLogChunk = %+v, %v", c1, err)
	}
	for _, c := range []store.NewLogChunk{chunk(e.ID, 21, 30, "logs/r/e/21-30"), chunk(e.ID, 11, 20, "logs/r/e/11-20")} {
		if _, err := s.InsertLogChunk(ctx, e.AttemptID, c, claimNow); err != nil {
			t.Fatal(err)
		}
	}

	all, err := s.ListLogChunks(ctx, e.ID, 0, 0, 10)
	if err != nil || len(all) != 3 || all[0].SeqFrom != 1 || all[1].SeqFrom != 11 || all[2].SeqFrom != 21 {
		t.Fatalf("ListLogChunks all = %+v, %v; want ordered 1,11,21", all, err)
	}
	if all[0].ObjectKey != "logs/r/e/1-10" || all[0].Bytes != 100 || !all[0].TsLast.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("chunk round trip = %+v", all[0])
	}
	mid, _ := s.ListLogChunks(ctx, e.ID, 12, 22, 10)
	if len(mid) != 2 || mid[0].SeqFrom != 11 || mid[1].SeqFrom != 21 {
		t.Fatalf("range 12-22 = %+v; want chunks 11-20 and 21-30", mid)
	}
	if after, _ := s.ListLogChunks(ctx, e.ID, 31, 0, 10); len(after) != 0 {
		t.Fatalf("range after the end = %+v", after)
	}
	if lim, _ := s.ListLogChunks(ctx, e.ID, 0, 0, 1); len(lim) != 1 {
		t.Fatalf("limit 1 returned %d", len(lim))
	}

	cases := []struct {
		name string
		att  string
		c    store.NewLogChunk
		want error
	}{
		{"duplicate seq_from", e.AttemptID, chunk(e.ID, 1, 5, "k"), store.ErrConflict},
		{"stale attempt", "stale", chunk(e.ID, 40, 50, "k"), store.ErrStale},
		{"missing execution", e.AttemptID, chunk("nope", 40, 50, "k"), store.ErrNotFound},
		{"inverted range", e.AttemptID, chunk(e.ID, 50, 40, "k"), store.ErrInvalid},
		{"no key", e.AttemptID, chunk(e.ID, 40, 50, ""), store.ErrInvalid},
	}
	for _, tc := range cases {
		if _, err := s.InsertLogChunk(ctx, tc.att, tc.c, claimNow); !errors.Is(err, tc.want) {
			t.Errorf("InsertLogChunk %s: %v; want %v", tc.name, err, tc.want)
		}
	}
	if all, _ := s.ListLogChunks(ctx, e.ID, 0, 0, 10); len(all) != 3 {
		t.Fatalf("rejected inserts changed the manifest: %d chunks", len(all))
	}
}

func testLogData(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)

	if _, err := s.GetLogData(ctx, "logs/p1/a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get missing: %v; want ErrNotFound", err)
	}
	binary := []byte{0, 1, 2, 0xff, 0, '\n'}
	puts := map[string][]byte{
		"logs/p1/a": binary, "logs/p1/b": []byte("b"), "logs/P1/c": []byte("c"),
		"logs/p10/x": []byte("x"), "logs/p%/y": []byte("y"), "logs/é1/z": []byte("z"),
	}
	for k, v := range puts {
		if err := s.PutLogData(ctx, k, v, t0); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	if got, err := s.GetLogData(ctx, "logs/p1/a"); err != nil || !bytes.Equal(got, binary) {
		t.Fatalf("binary round trip = %v, %v", got, err)
	}
	if err := s.PutLogData(ctx, "logs/p1/b", []byte("b2"), t0); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetLogData(ctx, "logs/p1/b"); string(got) != "b2" {
		t.Fatalf("overwrite = %q; want b2", got)
	}
	if err := s.PutLogData(ctx, "empty", nil, t0); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetLogData(ctx, "empty"); err != nil || len(got) != 0 {
		t.Fatalf("empty data = %v, %v", got, err)
	}
	if err := s.PutLogData(ctx, "", []byte("x"), t0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty key: %v; want ErrInvalid", err)
	}

	if _, err := s.DeleteLogDataPrefix(ctx, ""); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty prefix: %v; want ErrInvalid", err)
	}
	// Exact, case-sensitive prefix match; % is not a wildcard.
	for prefix, want := range map[string]int64{"logs/p1/": 2, "logs/p%": 1, "logs/é": 1, "nothing/": 0} {
		if n, err := s.DeleteLogDataPrefix(ctx, prefix); err != nil || n != want {
			t.Fatalf("DeleteLogDataPrefix(%q) = %d, %v; want %d", prefix, n, err, want)
		}
	}
	for _, keep := range []string{"logs/P1/c", "logs/p10/x", "empty"} {
		if _, err := s.GetLogData(ctx, keep); err != nil {
			t.Errorf("key %q must survive the deletes: %v", keep, err)
		}
	}
	if _, err := s.GetLogData(ctx, "logs/p1/a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted key still readable: %v", err)
	}
}
