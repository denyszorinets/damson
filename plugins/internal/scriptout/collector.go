// Package scriptout reads the output of a remote script: it emits each line to
// the run log, keeps the tail of each stream for the step outputs and parses the
// ##damson-output protocol (ARCHITECTURE.md section 6). It is shared by the
// plugins that run scripts on nodes (ssh, winrm) and uses only pkg/pluginsdk.
package scriptout

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"sync"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// TailSize is how much of each stream is kept for the outputs.
const TailSize = 64 << 10

const (
	protoKV   = "##damson-output "
	protoJSON = "##damson-output-json "
)

// Tail keeps the last TailSize bytes written to it.
type Tail struct{ b []byte }

func (t *Tail) AddLine(line string) {
	t.b = append(t.b, line...)
	t.b = append(t.b, '\n')
	if len(t.b) > 2*TailSize {
		t.b = append([]byte(nil), t.b[len(t.b)-TailSize:]...)
	}
}

func (t *Tail) String() string {
	if len(t.b) > TailSize {
		return string(t.b[len(t.b)-TailSize:])
	}
	return string(t.b)
}

// Collector reads one stream line by line, emits each line, keeps its tail
// and, for stdout, parses the output protocol (ARCHITECTURE.md section 6).
//
// A Collector is safe for concurrent use: Tail, Values and Stop may be called
// while Consume is still running, which happens when a step is cancelled and
// the caller stops waiting for the stream.
type Collector struct {
	emit   pluginsdk.Emitter
	stream pluginsdk.Stream
	proto  bool

	mu      sync.Mutex
	stopped bool
	tail    Tail
	values  map[string]any
}

// NewStdout collects a stdout stream: the output protocol is parsed and its
// lines are stripped from the log.
func NewStdout(emit pluginsdk.Emitter) *Collector {
	return &Collector{emit: emit, stream: pluginsdk.Stdout, proto: true}
}

// NewStderr collects a stderr stream, which is never parsed for the protocol.
func NewStderr(emit pluginsdk.Emitter) *Collector {
	return &Collector{emit: emit, stream: pluginsdk.Stderr}
}

// Tail is the last TailSize bytes of the stream, without protocol lines.
func (c *Collector) Tail() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tail.String()
}

// Values are the values set with ##damson-output lines, as a copy; nil when none.
func (c *Collector) Values() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		return nil
	}
	out := make(map[string]any, len(c.values))
	for k, v := range c.values {
		out[k] = v
	}
	return out
}

// Stop makes the collector drop every further line: nothing is emitted or kept
// after it returns. Call it when the step no longer waits for the stream.
func (c *Collector) Stop() {
	c.mu.Lock()
	c.stopped = true
	c.mu.Unlock()
}

// Consume reads r to EOF. A line longer than the read buffer is split into
// several lines, so a stream without newlines cannot grow memory unboundedly.
func (c *Collector) Consume(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		chunk, _, err := br.ReadLine()
		if err != nil {
			return
		}
		c.line(string(chunk))
	}
}

func (c *Collector) line(l string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return
	}
	if c.proto && c.parseProtocol(l) {
		return
	}
	c.tail.AddLine(l)
	c.emit.Log(c.stream, l)
}

// parseProtocol handles ##damson-output lines. It reports whether l was a
// protocol line, which is then stripped from the log stream. Malformed
// protocol lines are dropped with a warning that does not repeat their text.
func (c *Collector) parseProtocol(l string) bool {
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

func (c *Collector) set(k string, v any) {
	if c.values == nil {
		c.values = map[string]any{}
	}
	c.values[k] = v
}
