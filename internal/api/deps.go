package api

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/expr"
	"github.com/denyszorinets/damson/internal/store"
)

// Defaults of the SSE endpoints.
const (
	defaultPollInterval = time.Second
	defaultHeartbeat    = 15 * time.Second
	defaultMaxStreams   = 256
	defaultWriteTimeout = 10 * time.Second
)

// StreamConfig tunes the SSE endpoints.
type StreamConfig struct {
	// PollInterval is how often a stream re-reads the store to detect
	// changes. Default 1s.
	PollInterval time.Duration
	// Heartbeat is the interval of comment lines that keep idle streams alive
	// through proxies. Default 15s.
	Heartbeat time.Duration
	// MaxStreams bounds the SSE streams open at once, across all endpoints;
	// further requests get a 503 problem. Default 256.
	MaxStreams int
	// WriteTimeout is the deadline of each write to a client; a client that
	// cannot take an event in time is disconnected. Default 10s.
	WriteTimeout time.Duration
}

func (c StreamConfig) withDefaults() StreamConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = defaultPollInterval
	}
	if c.Heartbeat <= 0 {
		c.Heartbeat = defaultHeartbeat
	}
	if c.MaxStreams <= 0 {
		c.MaxStreams = defaultMaxStreams
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = defaultWriteTimeout
	}
	return c
}

// jitter spreads polls of many streams: d plus or minus 20%.
func jitter(d time.Duration) time.Duration {
	span := int64(d) / 5
	if span <= 0 {
		return d
	}
	return d - time.Duration(span) + time.Duration(rand.Int64N(2*span+1))
}

// deps is everything the job, run and execution endpoints need.
type deps struct {
	ds      DataStore
	plugins PluginCatalog
	// eng parses and validates specs and resolves step input; engErr is set
	// instead when it could not be built, and the endpoints then answer 503.
	eng     *engine.Engine
	engErr  error
	now     func() time.Time
	stream  StreamConfig
	tail    LogTail
	notify  RunNotifier
	advance RunEnqueuer
	slots   chan struct{} // semaphore of open SSE streams; nil means unlimited
}

// inTx runs fn in one store transaction and, if an orchestrator is wired,
// enqueues run.advance for runID in the same transaction. runID is read after
// fn, so fn may create the run.
func (d deps) inTx(ctx context.Context, fn func(tx store.Tx) (runID string, err error)) error {
	return d.ds.WithTx(ctx, func(tx store.Tx) error {
		id, err := fn(tx)
		if err != nil {
			return err
		}
		if d.advance == nil {
			return nil
		}
		return d.advance.Enqueue(ctx, tx, id)
	})
}

// acquireStream takes a stream slot. The returned release must be called
// exactly once, when the stream ends.
func (d deps) acquireStream() (release func(), err error) {
	if d.slots == nil {
		return func() {}, nil
	}
	select {
	case d.slots <- struct{}{}:
		return func() { <-d.slots }, nil
	default:
		return nil, errTooManyStreams
	}
}

func newDeps(cfg Config) deps {
	d := deps{
		ds: cfg.Data, plugins: cfg.Plugins, now: cfg.Now,
		stream: cfg.Stream.withDefaults(), tail: cfg.LogTail, notify: cfg.RunNotify, advance: cfg.Advance,
	}
	d.slots = make(chan struct{}, d.stream.MaxStreams)
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
