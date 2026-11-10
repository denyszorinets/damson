package scheduler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/controller/leader"
	"github.com/denyszorinets/damson/internal/controller/scheduler"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 {
		if err := goleak.Find(); err != nil {
			fmt.Fprintf(os.Stderr, "goleak: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

var t0 = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

type harness struct {
	st      *sqlite.Store
	jobID   string
	starter *fakeStarter
	el      *leader.Elector
}

// fakeStarter records what the duty asks for. Create does not insert a run; it
// returns an id, so the tests see exactly what the duty did.
type fakeStarter struct {
	mu         sync.Mutex
	prepared   []api.Trigger
	options    []map[string]any
	created    int
	prepareErr error
	createErr  error
}

func (f *fakeStarter) Prepare(_ context.Context, _ string, o map[string]any, trig api.Trigger) (api.Prepared, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared = append(f.prepared, trig)
	f.options = append(f.options, o)
	return api.Prepared{}, f.prepareErr
}

func (f *fakeStarter) Create(context.Context, store.Tx, api.Prepared) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return "", f.createErr
	}
	f.created++
	return fmt.Sprintf("run-%d", f.created), nil
}

func (f *fakeStarter) runs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	job, _, err := st.CreateJob(ctx, store.NewJob{ProjectID: store.DefaultProjectID, Name: "j", Spec: json.RawMessage(`{}`), Checksum: "c"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	el, err := leader.New(leader.Config{Store: st, Name: scheduler.LeaderName, Holder: "c1", Renew: 20 * time.Millisecond, TTL: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := el.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = el.Stop(context.Background()) })
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, ok := el.Term(); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never became leader")
		}
	}
	return &harness{st: st, jobID: job.ID, starter: &fakeStarter{}, el: el}
}

func (h *harness) add(t *testing.T, id string, mut func(*store.Schedule)) store.Schedule {
	t.Helper()
	next := t0
	sc := store.Schedule{ID: id, JobID: h.jobID, Name: id, Cron: "*/5 * * * *", Timezone: "UTC", Options: json.RawMessage(`{"env":"dev"}`),
		Enabled: true, Misfire: store.MisfireRunOnce, NextFireAt: &next}
	if mut != nil {
		mut(&sc)
	}
	ctx := store.WithAudit(context.Background(), store.AuditMeta{Actor: "test"})
	out, err := h.st.CreateSchedule(ctx, sc, t0)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *harness) duty(t *testing.T, ld scheduler.Leader, holder string, now time.Time) *scheduler.Scheduler {
	t.Helper()
	s, err := scheduler.New(scheduler.Config{Store: h.st, Starter: h.starter, Leader: ld, Holder: holder, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (h *harness) get(t *testing.T, id string) store.Schedule {
	t.Helper()
	sc, err := h.st.GetSchedule(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func TestFiresADueScheduleOnce(t *testing.T) {
	h := newHarness(t)
	h.add(t, "a", nil)
	now := t0.Add(2 * time.Second)
	d := h.duty(t, h.el, "c1", now)
	d.Once(context.Background())
	d.Once(context.Background())
	if h.starter.runs() != 1 {
		t.Fatalf("runs = %d, want 1", h.starter.runs())
	}
	sc := h.get(t, "a")
	if want := t0.Add(5 * time.Minute); !sc.NextFireAt.Equal(want) || sc.LastRunID != "run-1" || sc.LastError != "" || sc.LastFireAt == nil {
		t.Fatalf("schedule after fire = %+v", sc)
	}
	trig := h.starter.prepared[0]
	if trig.Type != api.TriggerSchedule || trig.ScheduleID != "a" || trig.FireAt != "2026-03-01T10:00:00Z" {
		t.Fatalf("trigger = %+v", trig)
	}
	if h.starter.options[0]["env"] != "dev" {
		t.Fatalf("options = %v", h.starter.options[0])
	}
}

func TestNotDueDisabledAndNotLeader(t *testing.T) {
	h := newHarness(t)
	h.add(t, "later", func(s *store.Schedule) { n := t0.Add(time.Hour); s.NextFireAt = &n })
	h.add(t, "off", func(s *store.Schedule) { s.Enabled, s.NextFireAt = false, nil })
	h.duty(t, h.el, "c1", t0.Add(time.Second)).Once(context.Background())
	if h.starter.runs() != 0 {
		t.Fatalf("fired %d runs, want none", h.starter.runs())
	}
	h.add(t, "due", nil)
	h.duty(t, noLeader{}, "c2", t0.Add(time.Second)).Once(context.Background())
	if h.starter.runs() != 0 {
		t.Fatal("a controller without the lease fired")
	}
}

type noLeader struct{}

func (noLeader) Term() (int64, bool) { return 0, false }

type fixedTerm int64

func (f fixedTerm) Term() (int64, bool) { return int64(f), true }

// A controller that believes it leads with a stale term is fenced by the store.
func TestDeposedLeaderIsFenced(t *testing.T) {
	h := newHarness(t)
	h.add(t, "a", nil)
	term, _ := h.el.Term()
	h.duty(t, fixedTerm(term+5), "c1", t0.Add(time.Second)).Once(context.Background())
	h.duty(t, h.el, "other", t0.Add(time.Second)).Once(context.Background())
	if h.starter.runs() != 0 {
		t.Fatalf("a fenced controller fired %d runs", h.starter.runs())
	}
	if sc := h.get(t, "a"); !sc.NextFireAt.Equal(t0) {
		t.Fatalf("a fenced controller moved next_fire_at to %v", sc.NextFireAt)
	}
}

func TestMisfirePolicies(t *testing.T) {
	h := newHarness(t)
	h.add(t, "once", func(s *store.Schedule) { s.Misfire = store.MisfireRunOnce })
	h.add(t, "skip", func(s *store.Schedule) { s.Misfire = store.MisfireSkip })
	late := t0.Add(3*time.Hour + 2*time.Minute)
	h.duty(t, h.el, "c1", late).Once(context.Background())
	if h.starter.runs() != 1 {
		t.Fatalf("runs = %d; want 1 (run_once fires, skip does not)", h.starter.runs())
	}
	once, skip := h.get(t, "once"), h.get(t, "skip")
	want := time.Date(2026, 3, 1, 13, 5, 0, 0, time.UTC)
	if !once.NextFireAt.Equal(want) || !skip.NextFireAt.Equal(want) {
		t.Fatalf("next fire = %v / %v, want %v (no catch-up of every missed time)", once.NextFireAt, skip.NextFireAt, want)
	}
	if once.LastRunID == "" || skip.LastRunID != "" || skip.LastError == "" {
		t.Fatalf("once = %+v, skip = %+v", once, skip)
	}
	// A skip schedule that is only a little late still runs.
	h2 := newHarness(t)
	h2.add(t, "skip", func(s *store.Schedule) { s.Misfire = store.MisfireSkip })
	h2.duty(t, h2.el, "c1", t0.Add(30*time.Second)).Once(context.Background())
	if h2.starter.runs() != 1 {
		t.Fatal("a slightly late skip schedule did not run")
	}
}

func TestStartErrorIsRecordedAndAdvances(t *testing.T) {
	h := newHarness(t)
	h.add(t, "a", nil)
	h.starter.prepareErr = &api.StartError{Msg: "the options are invalid (options.env: must be one of dev, prod)"}
	d := h.duty(t, h.el, "c1", t0.Add(time.Second))
	d.Once(context.Background())
	d.Once(context.Background())
	sc := h.get(t, "a")
	if sc.LastError != "the options are invalid (options.env: must be one of dev, prod)" || sc.LastRunID != "" {
		t.Fatalf("schedule = %+v", sc)
	}
	if !sc.NextFireAt.Equal(t0.Add(5 * time.Minute)) {
		t.Fatalf("next = %v; a failing schedule must still move on", sc.NextFireAt)
	}
	if len(h.starter.prepared) != 1 {
		t.Fatalf("prepared %d times; a moved-on schedule must not be retried", len(h.starter.prepared))
	}
}

func TestTransientErrorsLeaveItDue(t *testing.T) {
	h := newHarness(t)
	h.add(t, "a", nil)
	h.starter.prepareErr = errors.New("database is down")
	d := h.duty(t, h.el, "c1", t0.Add(time.Second))
	d.Once(context.Background())
	h.starter.prepareErr = nil
	h.starter.createErr = errors.New("insert failed")
	d.Once(context.Background())
	if sc := h.get(t, "a"); !sc.NextFireAt.Equal(t0) || sc.LastRunID != "" {
		t.Fatalf("a transient failure moved the schedule: %+v", sc)
	}
	h.starter.createErr = nil
	d.Once(context.Background())
	if h.starter.runs() != 1 {
		t.Fatalf("runs = %d after recovery, want 1", h.starter.runs())
	}
}

func TestBrokenExpressionStillFiresThisTime(t *testing.T) {
	h := newHarness(t)
	h.add(t, "never", func(s *store.Schedule) { s.Cron = "0 0 31 2 *" })
	h.duty(t, h.el, "c1", t0.Add(time.Second)).Once(context.Background())
	sc := h.get(t, "never")
	if h.starter.runs() != 1 || sc.LastError != "the schedule never fires again" || !sc.NextFireAt.Equal(t0.Add(time.Second+time.Hour)) {
		t.Fatalf("runs = %d, schedule = %+v", h.starter.runs(), sc)
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := scheduler.New(scheduler.Config{}); err == nil {
		t.Fatal("an empty config was accepted")
	}
}

func TestStartStop(t *testing.T) {
	h := newHarness(t)
	h.add(t, "a", nil)
	s, err := scheduler.New(scheduler.Config{Store: h.st, Starter: h.starter, Leader: h.el, Holder: "c1", Interval: 10 * time.Millisecond, Now: func() time.Time { return t0.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err == nil {
		t.Fatal("started twice")
	}
	for deadline := time.Now().Add(5 * time.Second); h.starter.runs() == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the loop never fired")
		}
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
