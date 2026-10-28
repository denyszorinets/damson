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
	var redacted []string
	out, _ := walkOutputs(obj, "", m, &redacted).(map[string]any)
	return out, redacted, nil
}

func walkOutputs(v any, path string, m *masker, redacted *[]string) any {
	switch t := v.(type) {
	case string:
		if m.Contains(t) {
			*redacted = append(*redacted, path)
			return maskText
		}
		return t
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		f, _ := t.Float64()
		return f
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			mk := m.Mask(k)
			out[mk] = walkOutputs(e, path+"."+mk, m, redacted)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = walkOutputs(e, fmt.Sprintf("%s[%d]", path, i), m, redacted)
		}
		return out
	}
	return v
}
