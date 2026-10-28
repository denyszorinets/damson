package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// jrFixture is an API server over a store with the built-in plugin "ssh"
// (input {"host": string}) and fast stream polling.
type jrFixture struct {
	st   store.Store
	h    http.Handler
	logs *syncBuffer
}

// syncBuffer is a log sink safe for concurrent handlers.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newJR(t *testing.T, st store.Store, tail api.LogTail) *jrFixture {
	t.Helper()
	reg, err := pluginreg.New([]pluginsdk.Manifest{apiManifest("ssh", "1.0.0")}, nil)
	require.NoError(t, err)
	logs := &syncBuffer{}
	srv := api.NewServer(api.Config{
		Addr: "127.0.0.1:0", Store: st, Data: st, Plugins: reg, UI: ui(), LogTail: tail,
		Now:    func() time.Time { return t0 },
		Stream: api.StreamConfig{PollInterval: 10 * time.Millisecond, Heartbeat: 20 * time.Millisecond},
		Logger: slog.New(slog.NewJSONHandler(logs, nil)),
	})
	return &jrFixture{st: st, h: srv.Handler(), logs: logs}
}

// call sends a request with an optional JSON body and returns the recorder.
func (f *jrFixture) call(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

// callOK asserts the status and decodes the body into out (if not nil).
func (f *jrFixture) callOK(t *testing.T, method, target, body string, want int, out any) {
	t.Helper()
	rec := f.call(t, method, target, body)
	require.Equal(t, want, rec.Code, "%s %s: %s", method, target, rec.Body.String())
	if out != nil {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), out), rec.Body.String())
	}
}

type problemBody struct {
	Type   string `json:"type"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Errors []struct {
		Message  string `json:"message"`
		Location string `json:"location"`
	} `json:"errors"`
}

func (p problemBody) locations() []string {
	var out []string
	for _, e := range p.Errors {
		out = append(out, e.Location)
	}
	return out
}

// specDoc renders a one-step job spec. options and with are JSON objects
// ("" omits options).
func specDoc(name, options, plugin, with string) string {
	opts := ""
	if options != "" {
		opts = `"options":` + options + `,`
	}
	return fmt.Sprintf(`{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":%q},`+
		`"spec":{%s"steps":[{"id":"a","plugin":%q,"with":%s}]}}`, name, opts, plugin, with)
}

func wrapSpec(spec string) string { return `{"spec":` + spec + `}` }

type jobDetailBody struct {
	Job struct {
		ID             string `json:"id"`
		Project        string `json:"project"`
		Name           string `json:"name"`
		CurrentVersion int    `json:"current_version"`
	} `json:"job"`
	Version struct {
		Version  int             `json:"version"`
		Checksum string          `json:"checksum"`
		Spec     json.RawMessage `json:"spec"`
	} `json:"version"`
	Warnings []struct {
		Path string `json:"path"`
	} `json:"warnings"`
}

type runBody struct {
	ID         string          `json:"id"`
	JobID      string          `json:"job_id"`
	Status     string          `json:"status"`
	Options    json.RawMessage `json:"options"`
	Trigger    json.RawMessage `json:"trigger"`
	JobVersion int             `json:"job_version"`
	Steps      []struct {
		Path       string          `json:"path"`
		Status     string          `json:"status"`
		Input      json.RawMessage `json:"input"`
		Executions []struct {
			ID      string `json:"id"`
			Attempt int    `json:"attempt"`
			State   string `json:"state"`
			Pool    string `json:"pool"`
		} `json:"executions"`
	} `json:"steps"`
}

// createJob posts a job and returns its detail.
func (f *jrFixture) createJob(t *testing.T, spec string) jobDetailBody {
	t.Helper()
	var d jobDetailBody
	f.callOK(t, http.MethodPost, "/api/v1/jobs", wrapSpec(spec), http.StatusCreated, &d)
	return d
}

// trigger starts a run of a job with a JSON body.
func (f *jrFixture) trigger(t *testing.T, jobID, body string) runBody {
	t.Helper()
	var r runBody
	f.callOK(t, http.MethodPost, "/api/v1/jobs/"+jobID+"/runs", body, http.StatusCreated, &r)
	return r
}
