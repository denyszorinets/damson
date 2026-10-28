package agent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"time"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Defaults for Config zero values.
const (
	DefaultHeartbeatInterval = 10 * time.Second // until the hello response says otherwise
	DefaultHelloTimeout      = 10 * time.Second
	DefaultDialTimeout       = 10 * time.Second
	slotsPerCPU              = 4 // ARCHITECTURE 7.3: max_concurrent defaults to CPU count x 4
)

// Timer is the part of time.Timer and time.Ticker the agent uses.
type Timer interface {
	C() <-chan time.Time
	Stop()
}

// Clock is the injectable timer source. The zero value uses real time.
type Clock struct {
	NewTimer  func(d time.Duration) Timer
	NewTicker func(d time.Duration) Timer
}

func (c Clock) timer(d time.Duration) Timer {
	if c.NewTimer != nil {
		return c.NewTimer(d)
	}
	t := time.NewTimer(d)
	return realTimer{t.C, func() { t.Stop() }}
}

func (c Clock) ticker(d time.Duration) Timer {
	if c.NewTicker != nil {
		return c.NewTicker(d)
	}
	t := time.NewTicker(d)
	return realTimer{t.C, t.Stop}
}

type realTimer struct {
	c    <-chan time.Time
	stop func()
}

func (r realTimer) C() <-chan time.Time { return r.c }
func (r realTimer) Stop() {
	if r.stop != nil {
		r.stop()
	}
}

// Config configures an Agent.
type Config struct {
	// ControllerURL is the ws:// or wss:// base URL of the controller. Required.
	ControllerURL string
	// Token is the pool token. Required. It is sent only in the upgrade
	// request and never logged.
	Token string
	// AgentID is the instance id (hostname plus boot nonce, see NewAgentID).
	// Required.
	AgentID string
	// Pool is the pool name announced in agent.hello. Required.
	Pool string
	// Slots is max_concurrent. Zero means DefaultSlots.
	Slots int
	// Labels are announced as given, plus os and arch unless set here.
	Labels map[string]string
	// Capabilities are announced in addition to the detected bin:<name> ones.
	Capabilities []string
	// Plugins supply the manifests announced in agent.hello.
	Plugins []pluginsdk.Plugin
	// Version is the agent version announced in agent.hello.
	Version string
	// ExecConfig tunes step execution, logs and the spool.
	ExecConfig

	Logger  *slog.Logger
	Clock   Clock
	Sampler Sampler
	// Rand returns a number in [0, 1) for jitter. Zero value: math/rand/v2.
	Rand func() float64
	// LookPath finds capability binaries. Zero value: exec.LookPath.
	LookPath func(string) (string, error)
	// HTTPClient is used for the WebSocket handshake. Zero value: default client.
	HTTPClient *http.Client

	// Zero values select the Default* constants.
	BackoffBase       time.Duration
	BackoffMax        time.Duration
	HeartbeatInterval time.Duration
	HelloTimeout      time.Duration
	DialTimeout       time.Duration
}

// DefaultSlots is the CPU count times four.
func DefaultSlots() int { return runtime.NumCPU() * slotsPerCPU }

var (
	errNoToken = errors.New("agent: Config.Token is required")
	errNoID    = errors.New("agent: Config.AgentID is required")
	errNoPool  = errors.New("agent: Config.Pool is required")
)

func (c *Config) applyDefaults() error {
	switch {
	case c.Token == "":
		return errNoToken
	case c.AgentID == "":
		return errNoID
	case c.Pool == "":
		return errNoPool
	}
	if _, err := connectURL(c.ControllerURL); err != nil {
		return err
	}
	if c.Slots <= 0 {
		c.Slots = DefaultSlots()
	}
	c.ExecConfig.applyDefaults()
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Sampler == nil {
		c.Sampler = NewSampler()
	}
	if c.Rand == nil {
		c.Rand = mrand.Float64
	}
	if c.LookPath == nil {
		c.LookPath = exec.LookPath
	}
	for _, d := range []struct {
		v *time.Duration
		d time.Duration
	}{
		{&c.BackoffBase, DefaultBackoffBase},
		{&c.BackoffMax, DefaultBackoffMax},
		{&c.HeartbeatInterval, DefaultHeartbeatInterval},
		{&c.HelloTimeout, DefaultHelloTimeout},
		{&c.DialTimeout, DefaultDialTimeout},
	} {
		if *d.v <= 0 {
			*d.v = d.d
		}
	}
	return nil
}

// connectURL appends the connect path to the controller base URL. The error
// never echoes the URL: it may carry credentials.
func connectURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		return "", errors.New("agent: Config.ControllerURL must be a ws:// or wss:// URL with a host")
	}
	return u.JoinPath(protocol.ConnectPath).String(), nil
}

// NewAgentID returns the hostname plus a boot nonce (ARCHITECTURE 7.1).
func NewAgentID() (string, error) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "agent"
	}
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generating agent id: %w", err)
	}
	return host + "-" + hex.EncodeToString(nonce[:]), nil
}

// capabilityBinaries are looked up on PATH and advertised as bin:<name>
// (ARCHITECTURE 16).
var capabilityBinaries = []string{"ansible", "ansible-playbook", "buildah", "git", "helm", "ssh"}

// capabilities returns the sorted, de-duplicated capability list.
func (c *Config) capabilities() []string {
	caps := slices.Clone(c.Capabilities)
	for _, b := range capabilityBinaries {
		if _, err := c.LookPath(b); err == nil {
			caps = append(caps, "bin:"+b)
		}
	}
	slices.Sort(caps)
	return slices.Compact(caps)
}

// labels returns the announced labels: os and arch defaults, overridden by
// the configured ones.
func (c *Config) labels() map[string]string {
	l := map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH}
	for k, v := range c.Labels {
		l[k] = v
	}
	return l
}

// manifests wraps the plugin manifests for the wire.
func (c *Config) manifests() []protocol.WireManifest {
	out := make([]protocol.WireManifest, 0, len(c.Plugins))
	for _, p := range c.Plugins {
		out = append(out, protocol.WireManifest{Manifest: p.Manifest()})
	}
	return out
}
