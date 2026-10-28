package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// sseWriter writes server-sent events and flushes after each one.
type sseWriter struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	timeout time.Duration
}

// sseResponse adapts a streaming body to Huma. The handler runs after the
// request was validated and the target checked, so errors are still plain
// problem+json; once streaming starts the status is 200.
func sseResponse(d deps, release func(), body func(ctx context.Context, s *sseWriter)) *huma.StreamResponse {
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		defer release()
		hctx.SetHeader("Content-Type", "text/event-stream")
		hctx.SetHeader("Cache-Control", "no-cache")
		hctx.SetHeader("X-Accel-Buffering", "no") // keep nginx from buffering
		_, w := humago.Unwrap(hctx)
		s := &sseWriter{w: w, rc: http.NewResponseController(w), timeout: d.stream.WriteTimeout}
		if s.comment("stream open") != nil {
			return
		}
		body(hctx.Context(), s)
	}}
}

// event sends one event. id may be empty; data is JSON encoded.
func (s *sseWriter) event(id, name string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encoding %s event: %w", name, err)
	}
	var sb strings.Builder
	if id != "" {
		sb.WriteString("id: " + id + "\n")
	}
	sb.WriteString("event: " + name + "\ndata: ")
	sb.Write(b)
	sb.WriteString("\n\n")
	return s.write(sb.String())
}

// comment sends a comment line, used as heartbeat.
func (s *sseWriter) comment(text string) error { return s.write(": " + text + "\n\n") }

func (s *sseWriter) write(str string) error {
	// A client that cannot take the event in time is dropped. Writers
	// without deadline support (tests) just write.
	if s.timeout > 0 {
		_ = s.rc.SetWriteDeadline(time.Now().Add(s.timeout))
	}
	if _, err := s.w.Write([]byte(str)); err != nil {
		return err //nolint:wrapcheck // a closed client connection, not worth wrapping
	}
	if err := s.rc.Flush(); err != nil {
		return err //nolint:wrapcheck // as above
	}
	return nil
}

// sseOperationResponses documents the stream in OpenAPI.
func sseOperationResponses(desc string) map[string]*huma.Response {
	return map[string]*huma.Response{
		"200": {
			Description: desc,
			Content:     map[string]*huma.MediaType{"text/event-stream": {Schema: &huma.Schema{Type: "string"}}},
		},
	}
}

// parseEventID reads a Last-Event-ID value; ok is false for anything that is
// not a non-negative integer.
func parseEventID(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
