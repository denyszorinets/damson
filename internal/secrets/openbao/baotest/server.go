// Package baotest is an in-memory OpenBao/Vault server for tests: KV v2 with
// check-and-set, custom metadata and LIST, plus token, AppRole and Kubernetes
// auth. It is a test helper and keeps the behaviors Damson relies on honest.
package baotest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// RootToken is a token that is valid from the start.
const RootToken = "s.root-token"

// Request is one request the server saw.
type Request struct {
	Method, Path, Token, Namespace string
}

type version struct {
	data    map[string]any
	created time.Time
	deleted bool
}

type entry struct {
	versions []version
	custom   map[string]string
	created  time.Time
	updated  time.Time
}

// Server is the fake.
type Server struct {
	TS *httptest.Server

	mu       sync.Mutex
	kv       map[string]*entry // "mount/path" (no data/metadata segment)
	tokens   map[string]time.Duration
	requests []Request
	logins   int

	// Lease is the lease_duration (seconds) a login or renewal answers.
	Lease int64
	// RoleID and SecretID are what the approle login accepts.
	RoleID, SecretID string
	// K8sRole and K8sJWT are what the kubernetes login accepts.
	K8sRole, K8sJWT string
	// Namespace, if set, must be sent in X-Vault-Namespace.
	Namespace string
	// Fail, if set, is called first for each request; a non-zero status is answered.
	Fail                    func(r Request) int
	revokeCalls, renewCalls int
}

// Start runs a plain-http server (tests use allow_plain_http).
func Start(t testing.TB) *Server {
	t.Helper()
	s := newServer()
	s.TS = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.TS.Close)
	return s
}

// StartTLS runs an https server; CAPEM is its certificate in PEM.
func StartTLS(t testing.TB) (*Server, []byte) {
	t.Helper()
	s := newServer()
	s.TS = httptest.NewTLSServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.TS.Close)
	return s, pemOf(s.TS)
}

func newServer() *Server {
	return &Server{
		kv: map[string]*entry{}, tokens: map[string]time.Duration{RootToken: 0}, Lease: 3600,
		RoleID: "role-1", SecretID: "secret-1", K8sRole: "damson", K8sJWT: "jwt-1",
	}
}

// URL is the server address.
func (s *Server) URL() string { return s.TS.URL }

// Requests returns a copy of the request log.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Logins returns how many logins succeeded.
func (s *Server) Logins() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logins
}

// AddToken makes token valid, with no lease.
func (s *Server) AddToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[token] = 0
}

// RevokeCalls counts revoke-self calls.
func (s *Server) RevokeCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revokeCalls
}

// RenewCalls counts renew-self calls.
func (s *Server) RenewCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renewCalls
}

// Revoke makes a token invalid, as if it expired.
func (s *Server) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
}

// SetValue writes a version straight into the store, bypassing Damson: data may
// hold any JSON value, and custom metadata is left alone.
func (s *Server) SetValue(path string, data map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.kv[path]
	if e == nil {
		e = &entry{custom: map[string]string{}, created: time.Now()}
		s.kv[path] = e
	}
	e.versions = append(e.versions, version{data: data, created: time.Now()})
	e.updated = time.Now()
}

// SoftDelete marks the current version of path deleted, like `bao kv delete`.
func (s *Server) SoftDelete(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.kv[path]; e != nil && len(e.versions) > 0 {
		e.versions[len(e.versions)-1].deleted = true
		e.updated = time.Now()
	}
}

// SetMetadataOnly creates an entry with metadata and no data version.
func (s *Server) SetMetadataOnly(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kv[path] = &entry{custom: map[string]string{}, created: time.Now(), updated: time.Now()}
}

// Paths lists the stored secret paths ("mount/prefix/scope/name").
func (s *Server) Paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for p := range s.kv {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	write(w, status, map[string]any{"errors": []string{msg}})
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req := Request{Method: r.Method, Path: r.URL.Path, Token: r.Header.Get("X-Vault-Token"), Namespace: r.Header.Get("X-Vault-Namespace")}
	s.requests = append(s.requests, req)
	if s.Fail != nil {
		if st := s.Fail(req); st != 0 {
			fail(w, st, "injected failure")
			return
		}
	}
	if s.Namespace != "" && req.Namespace != s.Namespace {
		fail(w, http.StatusBadRequest, "wrong namespace")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	if strings.HasPrefix(path, "auth/") && strings.HasSuffix(path, "/login") {
		s.login(w, r, path)
		return
	}
	if _, ok := s.tokens[req.Token]; !ok {
		fail(w, http.StatusForbidden, "permission denied")
		return
	}
	switch path {
	case "auth/token/lookup-self":
		write(w, 200, map[string]any{"data": map[string]any{"ttl": s.tokens[req.Token] / time.Second, "renewable": s.tokens[req.Token] > 0}})
	case "auth/token/renew-self":
		s.renewCalls++
		write(w, 200, map[string]any{"auth": map[string]any{"client_token": req.Token, "lease_duration": s.Lease, "renewable": true}})
	case "auth/token/revoke-self":
		s.revokeCalls++
		delete(s.tokens, req.Token)
		w.WriteHeader(http.StatusNoContent)
	default:
		s.kvRequest(w, r, path)
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request, path string) {
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)
	ok := false
	switch {
	case strings.HasPrefix(path, "auth/approle"):
		ok = body["role_id"] == s.RoleID && body["secret_id"] == s.SecretID
	case strings.HasPrefix(path, "auth/kubernetes"):
		ok = body["role"] == s.K8sRole && body["jwt"] == s.K8sJWT
	}
	if !ok {
		fail(w, http.StatusBadRequest, "invalid credentials")
		return
	}
	s.logins++
	tok := "s.login-" + time.Now().Format("150405.000000") + "-" + string(rune('a'+s.logins%26))
	s.tokens[tok] = time.Duration(s.Lease) * time.Second
	write(w, 200, map[string]any{"auth": map[string]any{"client_token": tok, "lease_duration": s.Lease, "renewable": true}})
}
