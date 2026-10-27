package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestNewEmitsJSONWithRoleAndHonoursLevel(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelWarn, "agent")
	l.Info("dropped")
	l.Warn("kept", "agent_id", "a1")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("output is not a single JSON record: %v: %q", err, buf.String())
	}
	if rec["msg"] != "kept" || rec["role"] != "agent" || rec["agent_id"] != "a1" {
		t.Fatalf("unexpected record: %v", rec)
	}
}
