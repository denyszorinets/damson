package controller

import (
	"errors"
	"log/slog"
	"time"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
)

// ConnectPath is the agent endpoint (ARCHITECTURE 7.1).
const ConnectPath = "/agent/v1/connect"

// Defaults for Config zero values.
const (
	DefaultHeartbeatInterval = 10 * time.Second // told to agents in the hello response
	DefaultFlushInterval     = 10 * time.Second // telemetry batch write
	DefaultInstanceInterval  = 5 * time.Second  // controller_instances heartbeat
	DefaultHelloTimeout      = 10 * time.Second // upgrade to agent.hello
	DefaultReconnectJitter   = 5 * time.Second  // agent.reconnect jitter bound
	DefaultStoreTimeout      = 5 * time.Second  // bound on one background store call
	logBatchBytes            = 64 * 1024
	logBatchIntervalMS       = 200
	maxAgentIDLen            = 128
)

// Ticker is the part of time.Ticker the gateway uses.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// Clock is the injectable time source. The zero value uses the real clock.
type Clock struct {
	Now       func() time.Time
	NewTicker func(d time.Duration) Ticker
}

func (c Clock) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Clock) ticker(d time.Duration) Ticker {
	if c.NewTicker != nil {
		return c.NewTicker(d)
	}
	return realTicker{time.NewTicker(d)}
}

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }

// Config configures a Gateway.
type Config struct {
	Store store.Store
	// ControllerID identifies this controller in controller_instances and as
	// the owner of agent rows. Required.
	ControllerID string
	// InternalURL is stored in controller_instances (ARCHITECTURE 8.5).
	InternalURL string
	// InsecureDev allows plain ws:// connections (no TLS, no
	// X-Forwarded-Proto: https). The pool token then travels in clear text.
	InsecureDev bool
	Clock       Clock
	Logger      *slog.Logger

	// Zero values select the Default* constants.
	HeartbeatInterval time.Duration
	FlushInterval     time.Duration
	InstanceInterval  time.Duration
	HelloTimeout      time.Duration
	ReconnectJitter   time.Duration
	StoreTimeout      time.Duration
}

var (
	errNoStore = errors.New("controller: Config.Store is required")
	errNoID    = errors.New("controller: Config.ControllerID is required")
)

func (c *Config) applyDefaults() error {
	if c.Store == nil {
		return errNoStore
	}
	if c.ControllerID == "" {
		return errNoID
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	for _, d := range []struct {
		v *time.Duration
		d time.Duration
	}{
		{&c.HeartbeatInterval, DefaultHeartbeatInterval},
		{&c.FlushInterval, DefaultFlushInterval},
		{&c.InstanceInterval, DefaultInstanceInterval},
		{&c.HelloTimeout, DefaultHelloTimeout},
		{&c.ReconnectJitter, DefaultReconnectJitter},
		{&c.StoreTimeout, DefaultStoreTimeout},
	} {
		if *d.v <= 0 {
			*d.v = d.d
		}
	}
	return nil
}

func limits() protocol.Limits {
	return protocol.Limits{
		MaxFrameBytes:      protocol.MaxFrameSize,
		LogBatchBytes:      logBatchBytes,
		LogBatchIntervalMS: logBatchIntervalMS,
	}
}
