package agent

import "time"

// Defaults for the executor settings of Config (ARCHITECTURE.md sections
// 7.3 and 10).
const (
	DefaultMemBufferBytes   int64 = 32 << 20  // unacknowledged logs held in memory
	DefaultSpoolBytes       int64 = 256 << 20 // bounded spool file beyond that
	DefaultLogBatchBytes    int64 = 64 << 10  // flush a batch at this size ...
	DefaultLogFlushInterval       = 200 * time.Millisecond
	DefaultCompleteRetry          = 2 * time.Second  // between step.complete attempts
	DefaultCancelGrace            = 5 * time.Second  // when step.cancel sets none
	completeCallTimeout           = 10 * time.Second // one step.complete round trip
)

// ExecConfig holds the executor settings of Config. Zero values select the
// Default* constants.
type ExecConfig struct {
	// PluginConfig is the operator settings per plugin name, passed as
	// pluginsdk.ExecRequest.Config (for example ssh: allow_insecure_host_keys).
	PluginConfig map[string]map[string]string
	// WorkRoot is where per-execution working directories are created.
	// Empty means the system temp dir.
	WorkRoot string
	// SpoolDir is where the log spool file lives. Empty means the system temp dir.
	SpoolDir string
	// MemBufferBytes and SpoolBytes bound the unacknowledged log buffer.
	MemBufferBytes int64
	SpoolBytes     int64
	// LogFlushInterval flushes a partial log batch; LogBatchBytes flushes a
	// full one.
	LogFlushInterval time.Duration
	LogBatchBytes    int64
	// CompleteRetry is the wait between step.complete attempts.
	CompleteRetry time.Duration
	// CancelGrace is how long a plugin gets to return after cancellation when
	// the request names no grace period.
	CancelGrace time.Duration
	// Now stamps log entries and timings. Zero value: time.Now.
	Now func() time.Time
}

func (c *ExecConfig) applyDefaults() {
	if c.MemBufferBytes <= 0 {
		c.MemBufferBytes = DefaultMemBufferBytes
	}
	if c.SpoolBytes <= 0 {
		c.SpoolBytes = DefaultSpoolBytes
	}
	if c.LogFlushInterval <= 0 {
		c.LogFlushInterval = DefaultLogFlushInterval
	}
	if c.LogBatchBytes <= 0 {
		c.LogBatchBytes = DefaultLogBatchBytes
	}
	if c.CompleteRetry <= 0 {
		c.CompleteRetry = DefaultCompleteRetry
	}
	if c.CancelGrace <= 0 {
		c.CancelGrace = DefaultCancelGrace
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}
