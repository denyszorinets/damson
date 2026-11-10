// Package schedule computes cron fire times. It is pure: the caller passes the
// current time in.
package schedule

import (
	"errors"
	"fmt"
	"strings"
	"time"

	// Embed the time zone database so a minimal container (no /usr/share/zoneinfo)
	// still resolves IANA names.
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

// parser reads standard 5-field cron plus the @hourly/@daily/... descriptors.
// Seconds are not supported: the scheduler ticks every 10 seconds.
var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Spec is a validated cron expression in a time zone.
type Spec struct {
	sched cron.Schedule
	loc   *time.Location
	// fixedHour is true when the expression names the hour (not "*" or "*/n"),
	// so a repeated wall-clock hour must not fire it twice. Expressions that
	// run every hour or more often keep running through the repeat, like
	// Vixie cron.
	fixedHour bool
}

// Parse validates expr and the IANA zone name tz ("UTC" when empty is not
// assumed: the caller decides the default).
func Parse(expr, tz string) (Spec, error) {
	expr = strings.TrimSpace(expr)
	switch {
	case expr == "":
		return Spec{}, errors.New("cron expression is empty")
	case strings.ContainsAny(expr, "\n\r") || strings.HasPrefix(expr, "TZ=") || strings.HasPrefix(expr, "CRON_TZ="):
		return Spec{}, errors.New("cron expression must be a plain expression; set the time zone in its own field")
	case strings.HasPrefix(expr, "@every"):
		return Spec{}, errors.New("@every is not supported; use a cron expression")
	}
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" || tz == "Local" {
		return Spec{}, fmt.Errorf("unknown time zone %q (use an IANA name such as Europe/Berlin or UTC)", tz)
	}
	s, err := parser.Parse("CRON_TZ=" + tz + " " + expr)
	if err != nil {
		return Spec{}, fmt.Errorf("invalid cron expression: %w", err)
	}
	return Spec{sched: s, loc: loc, fixedHour: namesHour(expr)}, nil
}

// namesHour reports whether the hour field of a 5-field expression, or a
// descriptor other than @hourly, pins the hour.
func namesHour(expr string) bool {
	f := strings.Fields(expr)
	if len(f) == 5 {
		return !strings.HasPrefix(f[1], "*")
	}
	return expr != "@hourly"
}

// Next returns the first fire time strictly after t, in UTC. ok is false if
// the expression never fires again (for example 31 February).
//
// An expression that names the hour fires once when clocks go back, at the
// first occurrence of its wall-clock time; one that does not exist when clocks
// go forward is skipped for that day. Expressions that run every hour or more
// often are not affected.
func (s Spec) Next(t time.Time) (next time.Time, ok bool) {
	n := s.sched.Next(t.In(s.loc))
	for i := 0; i < 4 && s.fixedHour && !n.IsZero() && s.repeated(n); i++ {
		n = s.sched.Next(n)
	}
	if n.IsZero() || (s.fixedHour && s.repeated(n)) {
		return time.Time{}, false
	}
	return n.UTC(), true
}

// repeated reports whether n is the second occurrence of its wall-clock time
// in the zone: the same wall time was already on the clock 30 or 60 minutes
// earlier (the DST shifts in use).
func (s Spec) repeated(n time.Time) bool {
	wall := func(t time.Time) [5]int {
		t = t.In(s.loc)
		return [5]int{t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute()}
	}
	w := wall(n)
	for _, back := range []time.Duration{30 * time.Minute, time.Hour} {
		if wall(n.Add(-back)) == w {
			return true
		}
	}
	return false
}
