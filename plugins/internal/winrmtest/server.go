// Package winrmtest is an in-process fake WinRM server for plugin tests. It
// speaks just enough WS-Man for the masterzen/winrm client (open shell, run a
// command, send stdin, receive output, signal, delete shell) over https with
// Basic authentication. It cannot run PowerShell: the payload the winrm plugin
// sends on stdin is interpreted as a tiny script instead, one statement per
// line:
//
//	echo TEXT     write TEXT to stdout
//	err TEXT      write TEXT to stderr
//	env NAME      write the value of the payload's environment variable NAME
//	exit N        end with exit code N
//	block         wait until the command is signaled or the shell is deleted
//
// Whether the real bootstrap script behaves as described has to be proven on a
// Windows host (see the documentation).
package winrmtest

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Options configures a Server.
type Options struct {
	User, Password string
	// CreateDelay delays the answer to open-shell, to cancel a step while it
	// is still waiting for the shell.
	CreateDelay time.Duration
}

// Server is a running fake WinRM endpoint; it stops when the test ends.
type Server struct {
	ts   *httptest.Server
	opts Options

	mu       sync.Mutex
	shells   map[string]*shell
	commands []string // command lines received
	payloads []Payload
	next     int
	// CAPEM is the PEM certificate of the server, for the tls_ca_pem setting.
	CAPEM string
}

// Payload is what the plugin sent on stdin.
type Payload struct {
	Env        map[string]string `json:"env"`
	Command    *string           `json:"command"`
	Powershell *string           `json:"powershell"`
}

type shell struct {
	mu       sync.Mutex
	cond     *sync.Cond
	stdin    strings.Builder
	stdinEnd bool
	stdout   strings.Builder
	stderr   strings.Builder
	done     bool
	exit     int
	signal   bool // terminate requested or shell deleted
	started  bool
}

var (
	reAction  = regexp.MustCompile(`<a:Action[^>]*>([^<]+)</a:Action>`)
	reShellID = regexp.MustCompile(`Name="ShellId"[^>]*>([^<]+)<`)
	reCommand = regexp.MustCompile(`(?s)<rsp:Command>(.*?)</rsp:Command>`)
	reStream  = regexp.MustCompile(`(?s)<rsp:Stream ([^>]*?)(?:/>|>([^<]*)</rsp:Stream>)`)
)

// Start runs a TLS server on 127.0.0.1.
func Start(t testing.TB, opts Options) *Server {
	t.Helper()
	s := &Server{opts: opts, shells: map[string]*shell{}}
	s.ts = httptest.NewUnstartedServer(http.HandlerFunc(s.handle))
	s.ts.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	s.ts.StartTLS()
	t.Cleanup(func() {
		s.wakeAll()
		s.ts.Close()
	})
	s.CAPEM = certPEM(s.ts)
	return s
}

// Host and Port are where the server listens.
func (s *Server) Host() string { h, _, _ := net.SplitHostPort(s.ts.Listener.Addr().String()); return h }

// Port is the TCP port.
func (s *Server) Port() int {
	_, p, _ := net.SplitHostPort(s.ts.Listener.Addr().String())
	n, _ := strconv.Atoi(p)
	return n
}

// Commands returns the command lines the server received.
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// Payloads returns the decoded stdin payloads the server received.
func (s *Server) Payloads() []Payload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Payload(nil), s.payloads...)
}

// ShellCount is the number of shells still open.
func (s *Server) ShellCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.shells)
}

func (s *Server) wakeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sh := range s.shells {
		sh.mu.Lock()
		sh.signal = true
		sh.cond.Broadcast()
		sh.mu.Unlock()
	}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	if !ok || user != s.opts.User || pass != s.opts.Password {
		w.Header().Set("WWW-Authenticate", `Basic realm="WSMAN"`)
		http.Error(w, "401 Unauthorized", http.StatusUnauthorized)
		return
	}
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	body := string(buf)
	m := reAction.FindStringSubmatch(body)
	if m == nil {
		http.Error(w, "no action", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/soap+xml;charset=UTF-8")
	switch {
	case strings.HasSuffix(m[1], "/transfer/Create"):
		s.create(w)
	case strings.HasSuffix(m[1], "/shell/Command"):
		s.command(w, body)
	case strings.HasSuffix(m[1], "/shell/Send"):
		s.send(w, body)
	case strings.HasSuffix(m[1], "/shell/Receive"):
		s.receive(w, body)
	case strings.HasSuffix(m[1], "/shell/Signal"):
		s.signal(w, body)
	case strings.HasSuffix(m[1], "/transfer/Delete"):
		s.delete(w, body)
	default:
		http.Error(w, "unsupported action "+m[1], http.StatusBadRequest)
	}
}

func envelope(action, body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" ` +
		`xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:rsp="http://schemas.microsoft.com/wbem/wsman/1/windows/shell">` +
		`<s:Header><a:Action>` + action + `</a:Action></s:Header><s:Body>` + body + `</s:Body></s:Envelope>`
}

func (s *Server) lookup(body string) *shell {
	m := reShellID.FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shells[m[1]]
}

func (s *Server) create(w http.ResponseWriter) {
	time.Sleep(s.opts.CreateDelay)
	s.mu.Lock()
	s.next++
	id := fmt.Sprintf("SHELL-%d", s.next)
	sh := &shell{}
	sh.cond = sync.NewCond(&sh.mu)
	s.shells[id] = sh
	s.mu.Unlock()
	_, _ = fmt.Fprint(w, envelope("http://schemas.xmlsoap.org/ws/2004/09/transfer/CreateResponse",
		`<rsp:Shell><rsp:ShellId>`+id+`</rsp:ShellId></rsp:Shell>`))
}

func (s *Server) command(w http.ResponseWriter, body string) {
	sh := s.lookup(body)
	if sh == nil {
		http.Error(w, "no such shell", http.StatusInternalServerError)
		return
	}
	line := ""
	if m := reCommand.FindStringSubmatch(body); m != nil {
		line = html.UnescapeString(strings.TrimSuffix(strings.TrimPrefix(html.UnescapeString(m[1]), "<![CDATA["), "]]>"))
	}
	s.mu.Lock()
	s.commands = append(s.commands, line)
	s.mu.Unlock()
	sh.mu.Lock()
	sh.started = true
	sh.mu.Unlock()
	go s.execute(sh)
	_, _ = fmt.Fprint(w, envelope("http://schemas.microsoft.com/wbem/wsman/1/windows/shell/CommandResponse",
		`<rsp:CommandResponse><rsp:CommandId>CMD-1</rsp:CommandId></rsp:CommandResponse>`))
}

func (s *Server) send(w http.ResponseWriter, body string) {
	sh := s.lookup(body)
	if sh == nil {
		http.Error(w, "no such shell", http.StatusInternalServerError)
		return
	}
	for _, m := range reStream.FindAllStringSubmatch(body, -1) {
		if !strings.Contains(m[1], `Name="stdin"`) {
			continue
		}
		raw, _ := base64.StdEncoding.DecodeString(m[2])
		sh.mu.Lock()
		sh.stdin.Write(raw)
		if strings.Contains(m[1], `End="true"`) {
			sh.stdinEnd = true
		}
		sh.cond.Broadcast()
		sh.mu.Unlock()
	}
	_, _ = fmt.Fprint(w, envelope("http://schemas.microsoft.com/wbem/wsman/1/windows/shell/SendResponse", ""))
}

// receive waits a short while for output or the end of the command.
func (s *Server) receive(w http.ResponseWriter, body string) {
	sh := s.lookup(body)
	if sh == nil {
		http.Error(w, "no such shell", http.StatusInternalServerError)
		return
	}
	deadline := time.Now().Add(300 * time.Millisecond)
	sh.mu.Lock()
	for sh.stdout.Len() == 0 && sh.stderr.Len() == 0 && !sh.done && time.Now().Before(deadline) {
		sh.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		sh.mu.Lock()
	}
	out, errs := sh.stdout.String(), sh.stderr.String()
	sh.stdout.Reset()
	sh.stderr.Reset()
	done, exit := sh.done, sh.exit
	sh.mu.Unlock()

	var b strings.Builder
	b.WriteString(`<rsp:ReceiveResponse>`)
	if out != "" {
		b.WriteString(`<rsp:Stream Name="stdout" CommandId="CMD-1">` + base64.StdEncoding.EncodeToString([]byte(out)) + `</rsp:Stream>`)
	}
	if errs != "" {
		b.WriteString(`<rsp:Stream Name="stderr" CommandId="CMD-1">` + base64.StdEncoding.EncodeToString([]byte(errs)) + `</rsp:Stream>`)
	}
	if done {
		b.WriteString(`<rsp:CommandState CommandId="CMD-1" State="http://schemas.microsoft.com/wbem/wsman/1/windows/shell/CommandState/Done">` +
			`<rsp:ExitCode>` + strconv.Itoa(exit) + `</rsp:ExitCode></rsp:CommandState>`)
	} else {
		b.WriteString(`<rsp:CommandState CommandId="CMD-1" State="http://schemas.microsoft.com/wbem/wsman/1/windows/shell/CommandState/Running"/>`)
	}
	b.WriteString(`</rsp:ReceiveResponse>`)
	_, _ = fmt.Fprint(w, envelope("http://schemas.microsoft.com/wbem/wsman/1/windows/shell/ReceiveResponse", b.String()))
}

func (s *Server) signal(w http.ResponseWriter, body string) {
	if sh := s.lookup(body); sh != nil {
		sh.mu.Lock()
		sh.signal = true
		sh.cond.Broadcast()
		sh.mu.Unlock()
	}
	_, _ = fmt.Fprint(w, envelope("http://schemas.microsoft.com/wbem/wsman/1/windows/shell/SignalResponse", ""))
}

func (s *Server) delete(w http.ResponseWriter, body string) {
	if m := reShellID.FindStringSubmatch(body); m != nil {
		s.mu.Lock()
		sh := s.shells[m[1]]
		delete(s.shells, m[1])
		s.mu.Unlock()
		if sh != nil {
			sh.mu.Lock()
			sh.signal = true
			sh.cond.Broadcast()
			sh.mu.Unlock()
		}
	}
	_, _ = fmt.Fprint(w, envelope("http://schemas.xmlsoap.org/ws/2004/09/transfer/DeleteResponse", ""))
}

// execute waits for the whole stdin, interprets it and finishes the command.
func (s *Server) execute(sh *shell) {
	sh.mu.Lock()
	for !sh.stdinEnd && !sh.signal {
		sh.cond.Wait()
	}
	raw := sh.stdin.String()
	sh.mu.Unlock()

	var p Payload
	if dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw)); err == nil {
		_ = json.Unmarshal(dec, &p)
	}
	s.mu.Lock()
	s.payloads = append(s.payloads, p)
	s.mu.Unlock()

	program := ""
	switch {
	case p.Command != nil:
		program = *p.Command
	case p.Powershell != nil:
		program = *p.Powershell
	}
	exit := 0
	write := func(stderr bool, text string) {
		sh.mu.Lock()
		if stderr {
			sh.stderr.WriteString(text + "\n")
		} else {
			sh.stdout.WriteString(text + "\n")
		}
		sh.mu.Unlock()
	}
run:
	for _, line := range strings.Split(program, "\n") {
		line = strings.TrimRight(line, "\r")
		verb, arg, _ := strings.Cut(line, " ")
		switch verb {
		case "echo":
			write(false, arg)
		case "err":
			write(true, arg)
		case "env":
			write(false, p.Env[arg])
		case "exit":
			exit, _ = strconv.Atoi(arg)
			break run
		case "block":
			sh.mu.Lock()
			for !sh.signal {
				sh.cond.Wait()
			}
			exit = 1
			sh.mu.Unlock()
			break run
		}
	}
	sh.mu.Lock()
	sh.done, sh.exit = true, exit
	sh.mu.Unlock()
}
