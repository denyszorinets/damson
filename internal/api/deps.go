package api

import (
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/expr"
)

// Defaults of the SSE endpoints.
const (
	defaultPollInterval = time.Second
	defaultHeartbeat    = 15 * time.Second
)

// StreamConfig tunes the SSE endpoints.
type StreamConfig struct {
	// PollInterval is how often a stream re-reads the store to detect
	// changes. Default 1s.
	PollInterval time.Duration
	// Heartbeat is the interval of comment lines that keep idle streams alive
	// through proxies. Default 15s.
	Heartbeat time.Duration
}

func (c StreamConfig) withDefaults() StreamConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = defaultPollInterval
	}
	if c.Heartbeat <= 0 {
		c.Heartbeat = defaultHeartbeat
	}
	return c
}

// deps is everything the job, run and execution endpoints need.
type deps struct {
	ds      DataStore
	plugins PluginCatalog
	// eng parses and validates specs and resolves step input; engErr is set
	// instead when it could not be built, and the endpoints then answer 503.
	eng    *engine.Engine
	engErr error
	now    func() time.Time
	stream StreamConfig
	tail   LogTail
	notify RunNotifier
}

func newDeps(cfg Config) deps {
	d := deps{
		ds: cfg.Data, plugins: cfg.Plugins, now: cfg.Now,
		stream: cfg.Stream.withDefaults(), tail: cfg.LogTail, notify: cfg.RunNotify,
	}
	if d.now == nil {
		d.now = time.Now
	}
	eng, err := engine.New(expr.Limits{})
	if err != nil {
		d.engErr = fmt.Errorf("building engine: %w", err)
		return d
	}
	d.eng = eng
	return d
}

// ready reports the error to answer with when the endpoints cannot serve.
func (d deps) ready() error {
	if d.ds == nil {
		return errUnavailable
	}
	if d.engErr != nil {
		return fmt.Errorf("%w: %w", errUnavailable, d.engErr)
	}
	return nil
}
