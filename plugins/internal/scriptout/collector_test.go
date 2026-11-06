package scriptout

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

type recorder struct{ lines []string }

func (r *recorder) Log(s pluginsdk.Stream, l string) { r.lines = append(r.lines, string(s)+":"+l) }
func (r *recorder) Progress(float64, string)         {}

func TestCollectorProtocol(t *testing.T) {
	rec := &recorder{}
	c := NewStdout(rec)
	c.Consume(strings.NewReader(strings.Join([]string{
		"plain",
		"##damson-output a=b=c",
		"##damson-output nokey",
		"##damson-output-json {\"x\":[1,2],\"y\":true}",
		"##damson-output-json not json",
		"  ##damson-output indented is plain",
		"crlf\r",
	}, "\n") + "\n"))
	if got := strings.Join(rec.lines, "|"); !strings.HasPrefix(got, "stdout:plain|system:") {
		t.Errorf("log = %s", got)
	}
	if c.Values()["a"] != "b=c" || c.Values()["y"] != true || len(c.Values()) != 3 {
		t.Errorf("values = %v", c.Values())
	}
	if want := "plain\n  ##damson-output indented is plain\ncrlf\n"; c.Tail() != want {
		t.Errorf("tail = %q, want %q", c.Tail(), want)
	}
	for _, l := range rec.lines {
		if strings.Contains(l, "nokey") || strings.Contains(l, "not json") {
			t.Errorf("warning repeats the malformed line: %q", l)
		}
	}
}

func TestTailKeepsLastBytes(t *testing.T) {
	var tl Tail
	line := strings.Repeat("x", 1023)
	for range 200 {
		tl.AddLine(line)
	}
	tl.AddLine("end")
	got := tl.String()
	if len(got) != TailSize || !strings.HasSuffix(got, "xxx\nend\n") {
		t.Errorf("tail len=%d suffix=%q", len(got), got[len(got)-10:])
	}
}

func TestStderrNotParsedForProtocol(t *testing.T) {
	rec := &recorder{}
	c := NewStderr(rec)
	c.Consume(strings.NewReader("##damson-output a=b\n"))
	if c.Values() != nil || len(rec.lines) != 1 {
		t.Errorf("stderr must not be parsed: values=%v lines=%v", c.Values(), rec.lines)
	}
}

// A node that ends lines with CRLF still yields clean protocol lines.
func TestCollectorCRLF(t *testing.T) {
	rec := &recorder{}
	c := NewStdout(rec)
	c.Consume(strings.NewReader("##damson-output k=v\r\n##damson-output-json {\"n\":2}\r\nplain\r\n"))
	if c.Values()["k"] != "v" || c.Values()["n"] != float64(2) {
		t.Errorf("values = %v", c.Values())
	}
	if got := c.Tail(); got != "plain\n" {
		t.Errorf("tail = %q", got)
	}
}

// Stop makes the collector drop what still arrives, and Tail and Values may be
// read while Consume is running (run with -race).
func TestCollectorStopAndConcurrentReads(t *testing.T) {
	rec := &recorder{}
	c := NewStdout(rec)
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() { c.Consume(pr); close(done) }()
	_, _ = io.WriteString(pw, "one\n##damson-output a=1\n")
	stopReads := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopReads:
				return
			default:
				_ = c.Tail()
				_ = c.Values()
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	c.Stop()
	_, _ = io.WriteString(pw, "late\n##damson-output b=2\n")
	_ = pw.Close()
	<-done
	close(stopReads)
	if got := c.Tail(); got != "one\n" {
		t.Errorf("tail = %q", got)
	}
	if v := c.Values(); v["a"] != "1" || v["b"] != nil {
		t.Errorf("values = %v", v)
	}
	// The returned map is a copy.
	c.Values()["x"] = 1
	if c.Values()["x"] != nil {
		t.Error("Values exposes the internal map")
	}
}
