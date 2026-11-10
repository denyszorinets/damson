package agent

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// Telemetry is one host resource sample reported in agent.heartbeat. Fields
// that cannot be measured are zero.
type Telemetry struct {
	CPUPercent  float64 // 0 to 100 across all cores, since the previous sample
	MemoryBytes uint64  // used memory
	Load1       float64 // one-minute load average
}

// Sampler reads host telemetry. Implementations must be safe for concurrent
// use and never fail: a value they cannot read is zero.
type Sampler interface {
	Sample() Telemetry
}

// NewSampler returns the /proc sampler on Linux and a zero sampler elsewhere.
func NewSampler() Sampler {
	if runtime.GOOS == "linux" {
		return &ProcSampler{Root: "/proc"}
	}
	return ZeroSampler{}
}

// ZeroSampler reports no telemetry (platforms without /proc).
type ZeroSampler struct{}

// Sample implements Sampler.
func (ZeroSampler) Sample() Telemetry { return Telemetry{} }

// ProcSampler samples a Linux-style proc filesystem rooted at Root.
type ProcSampler struct {
	Root string

	mu   sync.Mutex
	prev cpuTimes
	have bool
}

// Sample implements Sampler. CPU percent is the busy share between this call
// and the previous one, so the first call reports 0.
func (p *ProcSampler) Sample() Telemetry {
	var t Telemetry
	if cur, err := readProc(filepath.Join(p.Root, "stat"), parseCPUTimes); err == nil {
		p.mu.Lock()
		if p.have {
			t.CPUPercent = cpuPercent(p.prev, cur)
		}
		p.prev, p.have = cur, true
		p.mu.Unlock()
	}
	if used, err := readProc(filepath.Join(p.Root, "meminfo"), parseMemUsed); err == nil {
		t.MemoryBytes = used
	}
	if load, err := readProc(filepath.Join(p.Root, "loadavg"), parseLoad1); err == nil {
		t.Load1 = load
	}
	return t
}

func readProc[T any](path string, parse func(io.Reader) (T, error)) (T, error) {
	f, err := os.Open(path)
	if err != nil {
		var zero T
		return zero, err
	}
	defer func() { _ = f.Close() }()
	return parse(f)
}

// cpuTimes are cumulative jiffies from the aggregate "cpu" line.
type cpuTimes struct{ idle, total uint64 }

var errMalformed = errors.New("malformed proc data")

// parseCPUTimes reads the first "cpu " line of /proc/stat. Idle includes
// iowait; guest time is already part of user and nice, so it is not re-added.
func parseCPUTimes(r io.Reader) (cpuTimes, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var v [8]uint64 // user nice system idle iowait irq softirq steal
		for i := 0; i < len(v) && i+1 < len(f); i++ {
			n, err := strconv.ParseUint(f[i+1], 10, 64)
			if err != nil {
				return cpuTimes{}, fmt.Errorf("%w: cpu field %d: %w", errMalformed, i, err)
			}
			v[i] = n
		}
		var total uint64
		for _, n := range v {
			total += n
		}
		return cpuTimes{idle: v[3] + v[4], total: total}, nil
	}
	return cpuTimes{}, fmt.Errorf("%w: no cpu line", errMalformed)
}

// cpuPercent is the busy share between two samples, clamped to [0, 100].
func cpuPercent(prev, cur cpuTimes) float64 {
	if cur.total <= prev.total || cur.idle < prev.idle {
		return 0
	}
	total := float64(cur.total - prev.total)
	busy := total - float64(cur.idle-prev.idle)
	if busy < 0 {
		return 0
	}
	return min(100, busy/total*100)
}

// parseMemUsed returns MemTotal minus MemAvailable from /proc/meminfo, in bytes.
func parseMemUsed(r io.Reader) (uint64, error) {
	var total, avail uint64
	var haveTotal, haveAvail bool
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok || (name != "MemTotal" && name != "MemAvailable") {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			return 0, fmt.Errorf("%w: %s has no value", errMalformed, name)
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %s: %w", errMalformed, name, err)
		}
		if len(f) > 1 && strings.EqualFold(f[1], "kB") {
			n *= 1024
		}
		if name == "MemTotal" {
			total, haveTotal = n, true
		} else {
			avail, haveAvail = n, true
		}
	}
	if !haveTotal || !haveAvail {
		return 0, fmt.Errorf("%w: MemTotal or MemAvailable missing", errMalformed)
	}
	if avail > total {
		return 0, nil
	}
	return total - avail, nil
}

// parseLoad1 returns the first field of /proc/loadavg.
func parseLoad1(r io.Reader) (float64, error) {
	b, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, fmt.Errorf("%w: empty loadavg", errMalformed)
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("%w: loadavg %q", errMalformed, f[0])
	}
	return v, nil
}
