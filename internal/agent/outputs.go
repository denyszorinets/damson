package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// sanitizeOutputs decodes a plugin's JSON outputs and replaces every string
// value that contains a secret with ***. It returns the JSON paths it
// redacted so the caller can warn. Numbers keep their integer form.
//
// The decoded outputs are limited to maxOutputBytes in total (keys, strings
// and a small fixed cost per value), which keeps step.complete far below
// protocol.MaxFrameSize. Exceeding it is an error whose text names no value.
func sanitizeOutputs(raw json.RawMessage, m *masker) (map[string]any, []string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, nil, errors.New("plugin outputs are not valid JSON")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("plugin outputs are not a single JSON value")
	}
	if v == nil {
		return nil, nil, nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, nil, errors.New("plugin outputs are not a JSON object")
	}
	w := &outputWalker{m: m}
	out, _ := w.walk(obj, "").(map[string]any)
	if w.size > maxOutputBytes {
		return nil, nil, fmt.Errorf("plugin outputs exceed the limit of %d bytes", maxOutputBytes)
	}
	return out, w.redacted, nil
}

const (
	// maxOutputBytes caps the outputs of one step (1 MiB).
	maxOutputBytes = 1 << 20
	// valueCost is what a non-string value counts towards the cap.
	valueCost = 16
)

type outputWalker struct {
	m        *masker
	redacted []string
	size     int
}

func (w *outputWalker) walk(v any, path string) any {
	if w.size > maxOutputBytes { // stop early; the caller reports the error
		return nil
	}
	switch t := v.(type) {
	case string:
		w.size += len(t) + valueCost
		if w.m.Contains(t) {
			w.redacted = append(w.redacted, path)
			return maskText
		}
		return t
	case json.Number:
		w.size += valueCost
		if i, err := t.Int64(); err == nil {
			return i
		}
		f, _ := t.Float64()
		return f
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			mk := w.m.Mask(k)
			w.size += len(mk) + valueCost
			out[mk] = w.walk(e, path+"."+mk)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = w.walk(e, fmt.Sprintf("%s[%d]", path, i))
		}
		return out
	}
	w.size += valueCost
	return v
}
