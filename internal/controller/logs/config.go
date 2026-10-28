package logs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
)

// Defaults for Config zero values (ARCHITECTURE section 10).
const (
	DefaultRingLines       = 5000
	DefaultFlushInterval   = 5 * time.Second
	DefaultFlushBytes      = 1 << 20
	DefaultCheckInterval   = time.Second
	DefaultAttemptTTL      = time.Second
	DefaultStoreTimeout    = 5 * time.Second
	DefaultIdleTTL         = 15 * time.Minute
	DefaultSubscriberQueue = 1024
	// DefaultMaxPendingBytes bounds the unflushed lines held per execution.
	// Beyond it a batch is refused and the session closed, so the agent
	// resends after reconnecting. The total is this times the executions a
	// controller serves (bounded by agent slots).
	DefaultMaxPendingBytes = 4 << 20
	DefaultLookupRetries   = 2
	DefaultLookupRetryWait = 150 * time.Millisecond
	DefaultBackend         = "db"

	attemptCacheSize = 4096
	flushWorkers     = 8
	lineOverhead     = 48 // bytes counted per line on top of its text
)

// Ticker is the part of time.Ticker the component uses.
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

// Store is the part of the store the ingest needs.
type Store interface {
	store.LogStore
	GetExecutionByAttempt(ctx context.Context, attemptID string) (store.Execution, error)
	GetExecution(ctx context.Context, id string) (store.Execution, error)
	GetLogStamp(ctx context.Context, executionID string) (store.LogStamp, error)
}

// AckSender delivers a log.ack to the agent's current session. Finding the
// peer is the gateway's job. An error (typically: the agent is not connected)
// is ignored: the agent resends after reconnecting and gets a fresh ack for
// the duplicates.
type AckSender func(ctx context.Context, agentID string, ack protocol.LogAck) error

// Config configures an Ingest. Zero numeric values select the defaults.
type Config struct {
	Store  Store
	Acks   AckSender
	Clock  Clock
	Logger *slog.Logger

	// Backend names the LogStore backend recorded in log_chunks (default "db").
	Backend string

	RingLines       int           // lines kept per execution for live tail
	FlushInterval   time.Duration // age of the oldest unflushed line that triggers a chunk
	FlushBytes      int           // pending bytes that trigger a chunk
	CheckInterval   time.Duration // how often the loop looks for due chunks and sends acks
	AttemptTTL      time.Duration // how long an attempt-to-execution lookup is trusted
	StoreTimeout    time.Duration // bound on one store call or ack send
	IdleTTL         time.Duration // evicts executions that received nothing for this long
	SubscriberQueue int           // extra channel capacity of a live-tail subscriber
	MaxPendingBytes int           // unflushed bytes per execution before a batch is refused
	LookupRetries   int           // extra attempts when resolving an attempt fails transiently
	LookupRetryWait time.Duration // pause between those attempts
}

var (
	errNoStore = errors.New("logs: Config.Store is required")
	errNoAcks  = errors.New("logs: Config.Acks is required")
)

func (c *Config) applyDefaults() error {
	if c.Store == nil {
		return errNoStore
	}
	if c.Acks == nil {
		return errNoAcks
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Backend == "" {
		c.Backend = DefaultBackend
	}
	for _, d := range []struct {
		v *time.Duration
		d time.Duration
	}{
		{&c.FlushInterval, DefaultFlushInterval},
		{&c.CheckInterval, DefaultCheckInterval},
		{&c.AttemptTTL, DefaultAttemptTTL},
		{&c.StoreTimeout, DefaultStoreTimeout},
		{&c.IdleTTL, DefaultIdleTTL},
		{&c.LookupRetryWait, DefaultLookupRetryWait},
	} {
		if *d.v <= 0 {
			*d.v = d.d
		}
	}
	for _, n := range []struct {
		v *int
		d int
	}{
		{&c.RingLines, DefaultRingLines},
		{&c.FlushBytes, DefaultFlushBytes},
		{&c.SubscriberQueue, DefaultSubscriberQueue},
		{&c.MaxPendingBytes, DefaultMaxPendingBytes},
	} {
		if *n.v <= 0 {
			*n.v = n.d
		}
	}
	if c.LookupRetries < 0 {
		c.LookupRetries = 0
	} else if c.LookupRetries == 0 {
		c.LookupRetries = DefaultLookupRetries
	}
	return nil
}
