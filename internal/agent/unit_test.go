package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBackoffDelay(t *testing.T) {
	const base, limit = time.Second, 30 * time.Second
	tests := []struct {
		name    string
		attempt int
		r       float64
		want    time.Duration
	}{
		{"first retry, max jitter", 0, 1, time.Second},
		{"first retry, no jitter", 0, 0, 0},
		{"second, half", 1, 0.5, time.Second},
		{"third, max", 2, 1, 4 * time.Second},
		{"fifth, max", 4, 1, 16 * time.Second},
		{"sixth hits the cap", 5, 1, 30 * time.Second},
		{"far past the cap", 40, 1, 30 * time.Second},
		{"overflowing shift", 100, 1, 30 * time.Second},
		{"negative attempt", -3, 1, time.Second},
		{"r above 1 clamps", 0, 7, time.Second},
		{"r below 0 clamps", 3, -1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BackoffDelay(tt.attempt, base, limit, tt.r); got != tt.want {
				t.Errorf("BackoffDelay(%d, r=%v) = %v, want %v", tt.attempt, tt.r, got, tt.want)
			}
		})
	}
	if got := BackoffDelay(3, 0, limit, 1); got != 0 {
		t.Errorf("zero base = %v, want 0", got)
	}
}

func TestParseCPUTimes(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    cpuTimes
		wantErr bool
	}{
		{"full line", "cpu  100 10 50 800 40 5 5 0 0 0\ncpu0 1 1 1 1 1 1 1 1\n", cpuTimes{idle: 840, total: 1010}, false},
		{"short line", "cpu 1 2 3 4\n", cpuTimes{idle: 4, total: 10}, false},
		{"skips other lines", "intr 5\ncpu 1 1 1 1 1\n", cpuTimes{idle: 2, total: 5}, false},
		{"no cpu line", "intr 5\n", cpuTimes{}, true},
		{"garbage", "cpu a b c d e\n", cpuTimes{}, true},
		{"empty", "", cpuTimes{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCPUTimes(strings.NewReader(tt.in))
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("got %+v, %v; want %+v (err %v)", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestCPUPercent(t *testing.T) {
	tests := []struct {
		name      string
		prev, cur cpuTimes
		want      float64
	}{
		{"half busy", cpuTimes{idle: 100, total: 200}, cpuTimes{idle: 150, total: 300}, 50},
		{"idle", cpuTimes{idle: 100, total: 200}, cpuTimes{idle: 200, total: 300}, 0},
		{"fully busy", cpuTimes{idle: 100, total: 200}, cpuTimes{idle: 100, total: 300}, 100},
		{"no time passed", cpuTimes{idle: 1, total: 2}, cpuTimes{idle: 1, total: 2}, 0},
		{"counters went backwards", cpuTimes{idle: 10, total: 20}, cpuTimes{idle: 1, total: 2}, 0},
		{"idle exceeds total delta", cpuTimes{idle: 0, total: 10}, cpuTimes{idle: 50, total: 20}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cpuPercent(tt.prev, tt.cur); got != tt.want {
				t.Errorf("cpuPercent = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseMemUsed(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    uint64
		wantErr bool
	}{
		{"typical", "MemTotal:       16000 kB\nMemFree: 1 kB\nMemAvailable:    6000 kB\n", 10000 * 1024, false},
		{"no unit means bytes", "MemTotal: 100\nMemAvailable: 40\n", 60, false},
		{"available above total", "MemTotal: 1 kB\nMemAvailable: 2 kB\n", 0, false},
		{"missing available", "MemTotal: 1 kB\n", 0, true},
		{"bad number", "MemTotal: x kB\nMemAvailable: 1 kB\n", 0, true},
		{"empty value", "MemTotal:\nMemAvailable: 1 kB\n", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMemUsed(strings.NewReader(tt.in))
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("got %d, %v; want %d (err %v)", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestParseLoad1(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    float64
		wantErr bool
	}{
		{"typical", "0.52 0.40 0.30 1/200 12345\n", 0.52, false},
		{"empty", "", 0, true},
		{"garbage", "abc\n", 0, true},
		{"negative", "-1 0 0\n", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLoad1(strings.NewReader(tt.in))
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("got %v, %v; want %v (err %v)", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func writeProc(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProcSampler(t *testing.T) {
	dir := t.TempDir()
	writeProc(t, dir, "stat", "cpu 100 0 100 800 0 0 0 0\n")
	writeProc(t, dir, "meminfo", "MemTotal: 2048 kB\nMemAvailable: 1024 kB\n")
	writeProc(t, dir, "loadavg", "1.25 1 1 1/1 1\n")
	s := &ProcSampler{Root: dir}

	first := s.Sample()
	if first.CPUPercent != 0 || first.MemoryBytes != 1024*1024 || first.Load1 != 1.25 {
		t.Errorf("first sample = %+v", first)
	}
	writeProc(t, dir, "stat", "cpu 200 0 200 900 0 0 0 0\n") // +200 busy, +100 idle
	if got := s.Sample().CPUPercent; got < 66 || got > 67 {
		t.Errorf("cpu = %v, want about 66.7", got)
	}
}

func TestProcSamplerMissingFilesFallBackToZero(t *testing.T) {
	s := &ProcSampler{Root: filepath.Join(t.TempDir(), "absent")}
	if got := s.Sample(); got != (Telemetry{}) {
		t.Errorf("sample = %+v, want zero", got)
	}
	if got := (ZeroSampler{}).Sample(); got != (Telemetry{}) {
		t.Errorf("zero sampler = %+v", got)
	}
}

func TestRealSamplerSane(t *testing.T) {
	if runtime.GOOS != "linux" {
		if _, ok := NewSampler().(ZeroSampler); !ok {
			t.Fatalf("NewSampler on %s is not the zero sampler", runtime.GOOS)
		}
		return
	}
	s := NewSampler()
	s.Sample()
	got := s.Sample()
	if got.CPUPercent < 0 || got.CPUPercent > 100 || got.Load1 < 0 || got.MemoryBytes == 0 {
		t.Errorf("implausible telemetry: %+v", got)
	}
}

func TestConfigValidation(t *testing.T) {
	ok := Config{ControllerURL: "ws://h:1", Token: "t", AgentID: "a", Pool: "p"}
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"valid", func(*Config) {}, false},
		{"no token", func(c *Config) { c.Token = "" }, true},
		{"no id", func(c *Config) { c.AgentID = "" }, true},
		{"no pool", func(c *Config) { c.Pool = "" }, true},
		{"http url", func(c *Config) { c.ControllerURL = "http://h" }, true},
		{"no host", func(c *Config) { c.ControllerURL = "ws://" }, true},
		{"wss", func(c *Config) { c.ControllerURL = "wss://h/base" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ok
			tt.mutate(&c)
			if _, err := New(c); (err != nil) != tt.wantErr {
				t.Errorf("New err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
	a, err := New(ok)
	if err != nil {
		t.Fatal(err)
	}
	if a.cfg.Slots != runtime.NumCPU()*4 {
		t.Errorf("default slots = %d, want CPU*4 = %d", a.cfg.Slots, runtime.NumCPU()*4)
	}
	b, _ := New(Config{ControllerURL: "wss://h/base/", Token: "t", AgentID: "a", Pool: "p"})
	if b.connURL != "wss://h/base/agent/v1/connect" {
		t.Errorf("connect URL = %q", b.connURL)
	}
}

func TestCapabilitiesAndLabels(t *testing.T) {
	c := Config{
		Capabilities: []string{"bin:zzz", "bin:git"}, Labels: map[string]string{"env": "dev", "os": "custom"},
		LookPath: func(name string) (string, error) {
			if name == "git" || name == "helm" {
				return "/usr/bin/" + name, nil
			}
			return "", os.ErrNotExist
		},
	}
	got := strings.Join(c.capabilities(), ",")
	if got != "bin:git,bin:helm,bin:zzz" {
		t.Errorf("capabilities = %s", got)
	}
	l := c.labels()
	if l["env"] != "dev" || l["os"] != "custom" || l["arch"] != runtime.GOARCH {
		t.Errorf("labels = %v", l)
	}
}
