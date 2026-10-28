package logs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Line is one log entry as held in the ring buffer and in chunks. It is the
// API's LogLine, so Ingest satisfies api.LogTail.
type Line = api.LogLine

// Stream names stored in chunks and served to clients.
const (
	StreamOut = "out"
	StreamErr = "err"
	StreamSys = "sys"
)

func streamName(s pluginsdk.Stream) string {
	switch s {
	case pluginsdk.Stderr:
		return StreamErr
	case pluginsdk.System:
		return StreamSys
	default:
		return StreamOut
	}
}

// ObjectKey is the LogStore key of the chunk holding seqFrom..seqTo.
func ObjectKey(executionID string, seqFrom, seqTo int64) string {
	return fmt.Sprintf("logs/%s/%d-%d.ndjson", executionID, seqFrom, seqTo)
}

// chunkLine is the stored form: one JSON object per line,
// {"t": unix nanoseconds, "s": "out|err|sys", "l": "text"}. The sequence
// number is not stored: line i of a chunk has seq_from+i, because a chunk
// only ever holds a contiguous run.
type chunkLine struct {
	T int64  `json:"t"`
	S string `json:"s"`
	L string `json:"l"`
}

// EncodeChunk renders a contiguous run of lines as newline-delimited JSON.
func EncodeChunk(lines []Line) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, l := range lines {
		if err := enc.Encode(chunkLine{T: l.TS.UnixNano(), S: l.Stream, L: l.Line}); err != nil {
			return nil, fmt.Errorf("encoding log line %d: %w", l.Seq, err)
		}
	}
	return buf.Bytes(), nil
}

// ReadChunk parses a chunk object written by the chunk writer. seqFrom is
// the chunk's seq_from from the log_chunks row; line i gets seq seqFrom+i.
func ReadChunk(data []byte, seqFrom int64) ([]Line, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var out []Line
	for {
		var cl chunkLine
		err := dec.Decode(&cl)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, fmt.Errorf("decoding chunk line %d: %w", len(out)+1, err)
		}
		out = append(out, Line{Seq: seqFrom + int64(len(out)), TS: time.Unix(0, cl.T).UTC(), Stream: cl.S, Line: cl.L})
	}
}
