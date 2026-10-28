package ssh

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// tailSize is how much of each stream is kept for the outputs.
const tailSize = 64 << 10

const (
	protoKV   = "##damson-output "
	protoJSON = "##damson-output-json "
)

// tail keeps the last tailSize bytes written to it.
type tail struct{ b []byte }

func (t *tail) addLine(line string) {
	t.b = append(t.b, line...)
	t.b = append(t.b, '\n')
	if len(t.b) > 2*tailSize {
		t.b = append([]byte(nil), t.b[len(t.b)-tailSize:]...)
	}
}

func (t *tail) String() string {
	if len(t.b) > tailSize {
		return string(t.b[len(t.b)-tailSize:])
	}
	return string(t.b)
}

// collector reads one stream line by line, emits each line, keeps its tail
// and, for stdout, parses the output protocol (ARCHITECTURE.md section 6).
type collector struct {
	emit   pluginsdk.Emitter
	stream pluginsdk.Stream
	proto  bool
	tail   tail
	values map[string]any
}

// consume reads r to EOF. A line longer than the read buffer is split into
// several lines, so a stream without newlines cannot grow memory unboundedly.
func (c *collector) consume(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		chunk, _, err := br.ReadLine()
		if err != nil {
			return
		}
		c.line(string(chunk))
	}
}

func (c *collector) line(l string) {
	if c.proto && c.parseProtocol(l) {
		return
	}
	c.tail.addLine(l)
	c.emit.Log(c.stream, l)
}

// parseProtocol handles ##damson-output lines. It reports whether l was a
// protocol line, which is then stripped from the log stream. Malformed
// protocol lines are dropped with a warning that does not repeat their text.
func (c *collector) parseProtocol(l string) bool {
	switch {
	case strings.HasPrefix(l, protoKV):
		k, v, ok := strings.Cut(strings.TrimPrefix(l, protoKV), "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			c.emit.Log(pluginsdk.System, "ignored malformed ##damson-output line (expected key=value)")
			return true
		}
		c.set(k, v)
	case strings.HasPrefix(l, protoJSON):
		var obj map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(l, protoJSON)), &obj); err != nil {
			c.emit.Log(pluginsdk.System, "ignored malformed ##damson-output-json line (expected a JSON object)")
			return true
		}
		for k, v := range obj {
			c.set(k, v)
		}
	default:
		return false
	}
	return true
}

func (c *collector) set(k string, v any) {
	if c.values == nil {
		c.values = map[string]any{}
	}
	c.values[k] = v
}
