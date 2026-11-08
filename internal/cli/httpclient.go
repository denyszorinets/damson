package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	envServer      = "DAMSON_SERVER"
	envToken       = "DAMSON_TOKEN"
	defaultServer  = "http://127.0.0.1:8080"
	maxResponseCLI = 64 << 20
)

// httpFlags are the flags of every command that talks to a running controller.
type httpFlags struct {
	server    string
	tokenFile string
	json      bool
}

func (h *httpFlags) bind(fs *flag.FlagSet, environ []string) {
	def := envOf(environ, envServer)
	if def == "" {
		def = defaultServer
	}
	fs.StringVar(&h.server, "server", def, "controller URL (env "+envServer+")")
	fs.StringVar(&h.tokenFile, "token-file", "", "file holding the API token (default: env "+envToken+"; there is no token flag so it never shows in ps)")
	fs.BoolVar(&h.json, "json", false, "print the API answer as JSON")
}

func envOf(environ []string, name string) string {
	var v string
	for _, kv := range environ {
		if x, ok := strings.CutPrefix(kv, name+"="); ok {
			v = x
		}
	}
	return strings.TrimSpace(v)
}

// token reads the API token from the file or the environment.
func (h httpFlags) token(environ []string) (string, error) {
	if h.tokenFile != "" {
		b, err := os.ReadFile(h.tokenFile)
		if err != nil {
			return "", fmt.Errorf("reading the token file: %w", err)
		}
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
		return "", errors.New("the token file is empty")
	}
	if t := envOf(environ, envToken); t != "" {
		return t, nil
	}
	return "", fmt.Errorf("no token: set %s or pass --token-file", envToken)
}

// apiClient is a minimal client for the controller API.
type apiClient struct {
	base  string
	token string
	hc    *http.Client
}

func newAPIClient(h httpFlags, environ []string) (*apiClient, error) {
	u, err := url.Parse(h.server)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, fmt.Errorf("--server must be an http(s) URL without credentials; got %q", h.server)
	}
	tok, err := h.token(environ)
	if err != nil {
		return nil, err
	}
	return &apiClient{
		base: strings.TrimSuffix(u.String(), "/"), token: tok,
		hc: &http.Client{
			Timeout: 2 * time.Minute,
			// A redirect would carry the token to another host.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// problem is the RFC 9457 body of an error answer.
type problem struct {
	Status int    `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Errors []struct {
		Message  string `json:"message"`
		Location string `json:"location"`
	} `json:"errors"`
}

func (p problem) lines() []string {
	var out []string
	head := p.Detail
	if head == "" {
		head = p.Title
	}
	out = append(out, head)
	for _, e := range p.Errors {
		out = append(out, "  - "+e.Message)
	}
	return out
}

// do sends a request and returns the body of a 2xx answer, or an apiError.
func (c *apiClient) do(ctx context.Context, method, p string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding the request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+p, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling the controller: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseCLI))
	if err != nil {
		return nil, fmt.Errorf("reading the answer: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, &apiError{status: resp.StatusCode, body: data}
	}
	return data, nil
}

type apiError struct {
	status int
	body   []byte
}

func (e *apiError) Error() string {
	var p problem
	if json.Unmarshal(e.body, &p) == nil && (p.Detail != "" || p.Title != "") {
		return fmt.Sprintf("%d %s", e.status, strings.Join(p.lines(), "\n"))
	}
	return fmt.Sprintf("the controller answered %d", e.status)
}
