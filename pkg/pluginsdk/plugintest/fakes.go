// Package plugintest is the test harness and contract suite for Damson plugins.
//
// Run executes a plugin's Execute with a fake emitter, fake secrets and a temp
// WorkDir; RunContract is the suite every plugin must pass; CheckSource checks
// the "no init(), no mutable package state" convention.
package plugintest

import (
	"fmt"
	"strings"
	"sync"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// LogLine is one emitted log line.
type LogLine struct {
	Stream pluginsdk.Stream
	Line   string
}

// ProgressEvent is one emitted progress update.
type ProgressEvent struct {
	Pct float64
	Msg string
}

// FakeEmitter records everything a plugin emits. It does not mask, so a plugin
// that logs a secret value is caught by the contract suite.
type FakeEmitter struct {
	mu       sync.Mutex
	logs     []LogLine
	progress []ProgressEvent
}

// Log implements pluginsdk.Emitter.
func (e *FakeEmitter) Log(stream pluginsdk.Stream, line string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.logs = append(e.logs, LogLine{stream, line})
}

// Progress implements pluginsdk.Emitter.
func (e *FakeEmitter) Progress(pct float64, msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.progress = append(e.progress, ProgressEvent{pct, msg})
}

// Lines returns a copy of the recorded log lines.
func (e *FakeEmitter) Lines() []LogLine {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]LogLine(nil), e.logs...)
}

// Progresses returns a copy of the recorded progress events.
func (e *FakeEmitter) Progresses() []ProgressEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]ProgressEvent(nil), e.progress...)
}

// Text returns all log lines and progress messages joined by newlines.
func (e *FakeEmitter) Text() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var b strings.Builder
	for _, l := range e.logs {
		b.WriteString(l.Line + "\n")
	}
	for _, p := range e.progress {
		b.WriteString(p.Msg + "\n")
	}
	return b.String()
}

// FakeSecrets is a map-backed pluginsdk.SecretResolver.
type FakeSecrets map[string]string

// Resolve implements pluginsdk.SecretResolver.
func (s FakeSecrets) Resolve(ref string) (string, error) {
	v, ok := s[ref]
	if !ok {
		return "", fmt.Errorf("secret %q not found", ref)
	}
	return v, nil
}
