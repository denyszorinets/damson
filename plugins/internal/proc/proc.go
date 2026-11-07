// Package proc runs an external program for a built-in plugin: it streams its
// output to the step log, keeps the tail of stderr for error classification,
// kills the whole process group on cancel and maps a cancelled or timed out
// context to the matching step error. Plugins use it for ansible-playbook,
// buildah and helm.
package proc

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// maxLogLine bounds one buffered log line; a longer line is split.
const maxLogLine = 256 << 10

// stderrTail is how much of stderr Result keeps.
const stderrTail = 8 << 10

// Spec describes one command.
type Spec struct {
	Dir  string
	Env  []string // the whole environment; nothing is inherited
	Name string
	Args []string
}

// Result is the outcome of a command that ran to its end.
type Result struct {
	ExitCode int
	// Stderr is the last bytes of stderr (not masked: callers must not put it
	// in an error without the agent masker in front of it, which every step
	// error goes through).
	Stderr string
}

// Run runs the command and waits for it. A non-zero exit is not an error: it is
// in Result.ExitCode. An error is returned when the program is missing or could
// not start, or when ctx ended (cancelled or timeout).
func Run(ctx context.Context, s Spec, emit pluginsdk.Emitter) (Result, error) {
	cmd := exec.CommandContext(ctx, s.Name, s.Args...)
	cmd.Dir = s.Dir
	cmd.Env = s.Env
	prepare(cmd)
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{ExitCode: -1}, pluginsdk.NewError(pluginsdk.KindInternal, "preparing "+s.Name+" failed", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{ExitCode: -1}, pluginsdk.NewError(pluginsdk.KindInternal, "preparing "+s.Name+" failed", err)
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return Result{ExitCode: -1}, pluginsdk.NewError(pluginsdk.KindInternal, s.Name+" is not installed on this agent", err)
		}
		return Result{ExitCode: -1}, pluginsdk.NewError(pluginsdk.KindInternal, "starting "+s.Name+" failed", err)
	}
	tail := &tailBuf{max: stderrTail}
	var wg sync.WaitGroup
	wg.Go(func() { Pump(stdout, emit, pluginsdk.Stdout, nil) })
	wg.Go(func() { Pump(stderr, emit, pluginsdk.Stderr, tail) })
	wg.Wait()
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		kind := pluginsdk.KindCancelled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			kind = pluginsdk.KindTimeout
		}
		return Result{ExitCode: -1, Stderr: tail.String()}, pluginsdk.NewError(kind, s.Name+" was interrupted", ctx.Err())
	}
	var ee *exec.ExitError
	switch {
	case waitErr == nil:
		return Result{Stderr: tail.String()}, nil
	case errors.As(waitErr, &ee):
		return Result{ExitCode: ee.ExitCode(), Stderr: tail.String()}, nil
	}
	return Result{ExitCode: -1, Stderr: tail.String()}, pluginsdk.NewError(pluginsdk.KindInternal, "running "+s.Name+" failed", waitErr)
}

type tailBuf struct {
	max int
	b   []byte
}

func (t *tailBuf) add(line string) {
	t.b = append(t.b, line...)
	t.b = append(t.b, '\n')
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
}

func (t *tailBuf) String() string { return strings.TrimSpace(string(t.b)) }

// Pump logs each line of r until it ends. A line longer than maxLogLine is
// split. When tail is non-nil it also keeps the last bytes.
func Pump(r io.Reader, emit pluginsdk.Emitter, stream pluginsdk.Stream, tail *tailBuf) {
	br := bufio.NewReaderSize(r, 64<<10)
	var carry strings.Builder
	flush := func() {
		line := carry.String()
		carry.Reset()
		emit.Log(stream, line)
		if tail != nil {
			tail.add(line)
		}
	}
	for {
		chunk, prefix, err := br.ReadLine()
		carry.Write(chunk)
		if (!prefix && (len(chunk) > 0 || carry.Len() > 0)) || carry.Len() >= maxLogLine {
			flush()
		}
		if err != nil {
			if carry.Len() > 0 {
				flush()
			}
			return
		}
	}
}

// LineWriter turns writes into log lines (for output of an in-process helper
// such as the git checkout).
type LineWriter struct {
	Emit   pluginsdk.Emitter
	Stream pluginsdk.Stream
	mu     sync.Mutex
	buf    []byte
}

// Write implements io.Writer.
func (w *LineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexAny(string(w.buf), "\r\n")
		if i < 0 {
			break
		}
		if line := strings.TrimSpace(string(w.buf[:i])); line != "" {
			w.Emit.Log(w.Stream, line)
		}
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > maxLogLine {
		w.Emit.Log(w.Stream, string(w.buf))
		w.buf = nil
	}
	return len(p), nil
}
