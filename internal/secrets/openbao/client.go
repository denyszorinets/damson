// Package openbao is the secret backend that keeps secrets in an OpenBao or
// Vault KV v2 mount (ARCHITECTURE 9.2, ADR-0068). It speaks the HTTP API
// directly: about nine endpoints, no client library.
package openbao

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Auth methods.
const (
	AuthToken      = "token"
	AuthAppRole    = "approle"
	AuthKubernetes = "kubernetes"
)

// Defaults.
const (
	DefaultRequestTimeout = 10 * time.Second
	MaxCacheTTL           = 30 * time.Second
	maxResponseBytes      = 2 << 20
	maxErrorMessage       = 200
)

// Auth selects how the controller authenticates. Secret fields hold values
// (from the environment); the *File fields are re-read at every login so a
// rotated file is picked up.
type Auth struct {
	Method string

	Token     string // static token (AuthToken)
	TokenFile string // re-read on re-login

	RoleID, RoleIDFile string // AuthAppRole
	SecretID           string
	SecretIDFile       string

	Role      string // AuthKubernetes
	SATokenFP string // service account token file

	Mount string // auth mount path: approle or kubernetes by default
}

// Config configures a Client.
type Config struct {
	Address        string // https://host:port
	Namespace      string
	Mount          string // KV v2 mount, default "secret"
	Prefix         string // default "damson"
	ReadOnly       bool
	CAPEM          []byte
	AllowPlainHTTP bool
	RequestTimeout time.Duration
	CacheTTL       time.Duration
	Auth           Auth
	Logger         *slog.Logger
}

// Client talks to OpenBao. It owns a goroutine that keeps the token fresh:
// Start it before use and Stop it on shutdown.
type Client struct {
	cfg  Config
	base *url.URL
	hc   *http.Client
	log  *slog.Logger

	// sleep waits d or until ctx ends and reports whether the wait completed;
	// tests replace it.
	sleep func(ctx context.Context, d time.Duration) bool

	loginMu  sync.Mutex // serializes logins; held across the HTTP call
	mu       sync.Mutex // guards the fields below; never held across HTTP
	token    string
	ttl      time.Duration // lease of the current token; 0 = does not expire
	renew    bool
	loginErr error

	cancel context.CancelFunc
	done   chan struct{}
}

// New validates cfg and builds a stopped Client. It contacts nothing.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.Address)
	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
		return nil, errors.New("openbao: the address must be an http(s) URL")
	case u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
		return nil, errors.New("openbao: the address must hold a scheme and host only")
	case u.Scheme == "http" && !cfg.AllowPlainHTTP:
		return nil, errors.New("openbao: the address is plain http; use https or allow_plain_http")
	}
	if cfg.Mount == "" {
		cfg.Mount = "secret"
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = DefaultRequestTimeout
	}
	if cfg.CacheTTL < 0 || cfg.CacheTTL > MaxCacheTTL {
		return nil, fmt.Errorf("openbao: cache_ttl must be between 0 and %s", MaxCacheTTL)
	}
	if err := validatePath(cfg.Mount); err != nil {
		return nil, fmt.Errorf("openbao: mount: %w", err)
	}
	if err := validatePath(cfg.Prefix); err != nil {
		return nil, fmt.Errorf("openbao: prefix: %w", err)
	}
	if cfg.Auth.Mount != "" {
		if err := validatePath(cfg.Auth.Mount); err != nil {
			return nil, fmt.Errorf("openbao: auth mount: %w", err)
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if u.Scheme == "https" {
		pool, perr := x509.SystemCertPool()
		if perr != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if len(cfg.CAPEM) > 0 && !pool.AppendCertsFromPEM(cfg.CAPEM) {
			return nil, errors.New("openbao: tls_ca_file holds no certificate")
		}
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	c := &Client{cfg: cfg, base: u, log: log.With("component", "openbao"), sleep: realSleep}
	c.hc = &http.Client{
		Transport: tr, Timeout: cfg.RequestTimeout,
		// A redirect would forward the token header to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return c, nil
}

func realSleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// validatePath checks a mount or prefix: 1 to 8 segments of [A-Za-z0-9._-],
// starting with a letter or digit.
func validatePath(p string) error {
	segs := strings.Split(p, "/")
	if len(segs) > 8 {
		return errors.New("more than 8 segments")
	}
	for _, s := range segs {
		if s == "" || strings.Contains(s, "..") || !isSegment(s) {
			return fmt.Errorf("bad segment %q", s)
		}
	}
	return nil
}

func isSegment(s string) bool {
	for i, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || (i > 0 && (r == '.' || r == '_' || r == '-'))
		if !ok {
			return false
		}
	}
	return s != ""
}

// errMalformed marks an answer no retry fixes: too large or not JSON.
var errMalformed = errors.New("malformed answer")

// apiError is a non-2xx answer. Its text holds the status and the first
// message OpenBao gave, never the body.
type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string {
	if e.Msg == "" {
		return fmt.Sprintf("openbao answered %d", e.Status)
	}
	return fmt.Sprintf("openbao answered %d: %s", e.Status, e.Msg)
}

func (e *apiError) transient() bool {
	return e.Status == http.StatusTooManyRequests || (e.Status >= 500 && e.Status != http.StatusNotImplemented)
}

func statusOf(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// do sends one request with the current token. A 403 triggers one re-login
// and one retry; read requests (retry) are repeated on transient failures.
func (c *Client) do(ctx context.Context, method, path string, body, out any, retry bool) error {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding the request: %w", err)
		}
		payload = b
		defer clear(payload)
	}
	backoff := []time.Duration{0, 100 * time.Millisecond, 400 * time.Millisecond}
	attempts := 1
	if retry {
		attempts = len(backoff)
	}
	var err error
	relogged := false
	for i := 0; i < attempts; i++ {
		if i > 0 && !c.sleep(ctx, backoff[i]) {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			return err
		}
		tok := c.currentToken()
		err = c.send(ctx, method, path, tok, payload, out)
		if statusOf(err) == http.StatusForbidden && !relogged {
			relogged = true
			switch lerr := c.relogin(ctx, tok); {
			case lerr == nil:
				i-- // the retry after a login does not count
				continue
			case errors.Is(lerr, errTokenValid):
				// The policy denies this request; the 403 stands.
			case !definite(lerr):
				return fmt.Errorf("re-authenticating to openbao: %w", lerr) // transient: do not make the secret look unusable
			}
		}
		var ae *apiError
		transient := errors.As(err, &ae) && ae.transient()
		if err == nil || !retry || (!transient && !isNetwork(err)) {
			return err
		}
	}
	return err
}

func isNetwork(err error) bool {
	var ne interface{ Timeout() bool }
	var ue *url.Error
	return errors.As(err, &ue) || errors.As(err, &ne)
}

func (c *Client) send(ctx context.Context, method, path, token string, payload []byte, out any) error {
	u := *c.base
	u.Path = "/v1/" + strings.TrimPrefix(path, "/")
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return fmt.Errorf("building the request: %w", err)
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if c.cfg.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", c.cfg.Namespace)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		// The URL holds a mount and a path, never a secret value; the token is a header.
		return fmt.Errorf("calling openbao: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("reading the openbao answer: %w", err)
	}
	defer clear(data)
	if len(data) > maxResponseBytes {
		return fmt.Errorf("the openbao answer is over %d bytes: %w", maxResponseBytes, errMalformed)
	}
	if resp.StatusCode/100 != 2 {
		return &apiError{Status: resp.StatusCode, Msg: firstError(data)}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decoding the openbao answer: %w", errMalformed)
		}
	}
	return nil
}

// firstError returns the first message of an OpenBao error body, truncated.
func firstError(data []byte) string {
	var e struct {
		Errors []string `json:"errors"`
	}
	if json.Unmarshal(data, &e) != nil || len(e.Errors) == 0 {
		return ""
	}
	m := e.Errors[0]
	if len(m) > maxErrorMessage {
		m = m[:maxErrorMessage]
	}
	return m
}

func readSecretFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return v, nil
}
