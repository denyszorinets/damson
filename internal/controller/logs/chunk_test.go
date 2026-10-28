package logs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChunk_EncodeReadRoundTrip(t *testing.T) {
	in := []Line{
		{Seq: 10, TS: t0, Stream: StreamOut, Line: "plain"},
		{Seq: 11, TS: t0.Add(time.Second), Stream: StreamErr, Line: "quote \" newline-free <tag> é \t tab"},
		{Seq: 12, TS: t0.Add(2 * time.Second), Stream: StreamSys, Line: ""},
	}
	data, err := EncodeChunk(in)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"s":"err"`)
	assert.Equal(t, 3, len(splitLines(data)), "one JSON object per line")

	out, err := ReadChunk(data, 10)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

func TestReadChunk_RejectsGarbage(t *testing.T) {
	_, err := ReadChunk([]byte("{\"t\":1,\"s\":\"out\",\"l\":\"a\"}\nnot json\n"), 1)
	require.Error(t, err)
}

func TestObjectKey(t *testing.T) {
	assert.Equal(t, "logs/e1/5-9.ndjson", ObjectKey("e1", 5, 9))
}

func TestContiguousRuns(t *testing.T) {
	line := func(s int64) Line { return Line{Seq: s} }
	tests := []struct {
		name string
		in   []int64
		want [][]int64
	}{
		{"one run", []int64{1, 2, 3}, [][]int64{{1, 2, 3}}},
		{"hole splits", []int64{1, 2, 5, 6, 9}, [][]int64{{1, 2}, {5, 6}, {9}}},
		{"single", []int64{4}, [][]int64{{4}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var lines []Line
			for _, s := range tc.in {
				lines = append(lines, line(s))
			}
			var got [][]int64
			for _, r := range contiguousRuns(lines) {
				got = append(got, seqs(r))
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestAttemptCache(t *testing.T) {
	c := newAttemptCache(2, 10*time.Second)
	c.put("a", "e1", t0)
	c.put("b", "e2", t0)

	id, ok := c.get("a", t0.Add(9*time.Second))
	assert.True(t, ok)
	assert.Equal(t, "e1", id)
	_, ok = c.get("a", t0.Add(10*time.Second))
	assert.False(t, ok, "entries expire at the TTL")

	c.put("c", "e3", t0)
	c.put("d", "e4", t0) // evicts the least recently used: b
	_, ok = c.get("b", t0)
	assert.False(t, ok)
	assert.Equal(t, 2, c.len())

	c.put("stale", "", t0)
	_, ok = c.get("stale", t0.Add(1999*time.Millisecond))
	assert.True(t, ok)
	_, ok = c.get("stale", t0.Add(2*time.Second))
	assert.False(t, ok, "unknown attempts are cached for a fifth of the TTL")

	c.put("c", "e3", t0)
	c.forget("c")
	_, ok = c.get("c", t0)
	assert.False(t, ok)
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	return out
}
