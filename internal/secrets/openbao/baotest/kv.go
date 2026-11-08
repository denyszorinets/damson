package baotest

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"time"
)

func pemOf(ts *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw})
}

func (e *entry) current() (version, int64) {
	n := int64(len(e.versions))
	if n == 0 {
		return version{}, 0
	}
	return e.versions[n-1], n
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (s *Server) kvRequest(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 3 {
		fail(w, http.StatusNotFound, "unsupported path")
		return
	}
	mount, kind, rest := parts[0], parts[1], parts[2]
	key := mount + "/" + strings.TrimSuffix(rest, "/")
	switch kind {
	case "data":
		s.data(w, r, key)
	case "metadata":
		if r.Method == "LIST" || r.URL.Query().Get("list") == "true" {
			s.list(w, mount, rest)
			return
		}
		s.metadata(w, r, key)
	default:
		fail(w, http.StatusNotFound, "unsupported path")
	}
}

func (s *Server) data(w http.ResponseWriter, r *http.Request, key string) {
	e := s.kv[key]
	switch r.Method {
	case http.MethodGet:
		if e == nil {
			fail(w, http.StatusNotFound, "")
			return
		}
		v, n := e.current()
		if n == 0 || v.deleted {
			write(w, http.StatusNotFound, map[string]any{"data": map[string]any{"metadata": map[string]any{"version": n}}})
			return
		}
		write(w, 200, map[string]any{"data": map[string]any{"data": v.data, "metadata": map[string]any{"version": n, "created_time": stamp(v.created)}}})
	case http.MethodPost, http.MethodPut:
		var body struct {
			Options struct {
				CAS *int64 `json:"cas"`
			} `json:"options"`
			Data map[string]any `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(w, http.StatusBadRequest, "bad json")
			return
		}
		var cur int64
		if e != nil {
			_, cur = e.current()
		}
		if body.Options.CAS != nil && *body.Options.CAS != cur {
			fail(w, http.StatusBadRequest, "check-and-set parameter did not match the current version")
			return
		}
		if e == nil {
			e = &entry{custom: map[string]string{}, created: time.Now()}
			s.kv[key] = e
		}
		now := time.Now()
		e.versions = append(e.versions, version{data: body.Data, created: now})
		e.updated = now
		write(w, 200, map[string]any{"data": map[string]any{"version": cur + 1, "created_time": stamp(now)}})
	default:
		fail(w, http.StatusMethodNotAllowed, "unsupported method")
	}
}

func (s *Server) metadata(w http.ResponseWriter, r *http.Request, key string) {
	e := s.kv[key]
	switch r.Method {
	case http.MethodGet:
		if e == nil {
			fail(w, http.StatusNotFound, "")
			return
		}
		_, n := e.current()
		vs := map[string]any{}
		for i, v := range e.versions {
			dt := ""
			if v.deleted {
				dt = stamp(e.updated)
			}
			vs[strconv.Itoa(i+1)] = map[string]any{"created_time": stamp(v.created), "deletion_time": dt, "destroyed": false}
		}
		write(w, 200, map[string]any{"data": map[string]any{
			"current_version": n, "created_time": stamp(e.created), "updated_time": stamp(e.updated),
			"custom_metadata": e.custom, "versions": vs,
		}})
	case http.MethodPost, http.MethodPut:
		if e == nil {
			e = &entry{custom: map[string]string{}, created: time.Now()}
			s.kv[key] = e
		}
		var body struct {
			Custom map[string]string `json:"custom_metadata"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(w, http.StatusBadRequest, "bad json")
			return
		}
		e.custom = body.Custom
		e.updated = time.Now()
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		delete(s.kv, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		fail(w, http.StatusMethodNotAllowed, "unsupported method")
	}
}

func (s *Server) list(w http.ResponseWriter, mount, rest string) {
	folder := mount + "/" + strings.TrimSuffix(rest, "/") + "/"
	if strings.TrimSuffix(rest, "/") == "" {
		folder = mount + "/"
	}
	seen := map[string]bool{}
	for k := range s.kv {
		after, ok := strings.CutPrefix(k, folder)
		if !ok || after == "" {
			continue
		}
		if i := strings.Index(after, "/"); i >= 0 {
			seen[after[:i+1]] = true
		} else {
			seen[after] = true
		}
	}
	if len(seen) == 0 {
		fail(w, http.StatusNotFound, "")
		return
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	write(w, 200, map[string]any{"data": map[string]any{"keys": keys}})
}
