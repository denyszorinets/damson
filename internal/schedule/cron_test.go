package schedule

import (
	"strings"
	"testing"
	"time"
)

func TestParseRejects(t *testing.T) {
	tests := []struct{ name, expr, tz, want string }{
		{"empty", "", "UTC", "empty"},
		{"garbage", "not a cron", "UTC", "invalid cron"},
		{"six fields", "0 */5 * * * *", "UTC", "invalid cron"},
		{"every", "@every 5m", "UTC", "@every"},
		{"embedded tz", "CRON_TZ=Asia/Tokyo * * * * *", "UTC", "plain expression"},
		{"newline", "* * * * *\n* * * * *", "UTC", "plain expression"},
		{"no zone", "* * * * *", "", "unknown time zone"},
		{"local", "* * * * *", "Local", "unknown time zone"},
		{"bad zone", "* * * * *", "Mars/Base", "unknown time zone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.expr, tt.tz); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Parse(%q, %q) = %v, want error containing %q", tt.expr, tt.tz, err, tt.want)
			}
		})
	}
}

func TestNext(t *testing.T) {
	utc := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	tests := []struct {
		name, expr, tz, after, want string
	}{
		{"every five minutes", "*/5 * * * *", "UTC", "2026-03-01T10:02:00Z", "2026-03-01T10:05:00Z"},
		{"strictly after", "*/5 * * * *", "UTC", "2026-03-01T10:05:00Z", "2026-03-01T10:10:00Z"},
		{"daily descriptor", "@daily", "UTC", "2026-03-01T10:00:00Z", "2026-03-02T00:00:00Z"},
		{"zone offset", "0 9 * * *", "Europe/Berlin", "2026-01-10T00:00:00Z", "2026-01-10T08:00:00Z"},
		{"zone offset in summer", "0 9 * * *", "Europe/Berlin", "2026-07-10T00:00:00Z", "2026-07-10T07:00:00Z"},
		// Berlin springs forward on 2026-03-29: 02:00 does not exist. The
		// 02:30 job does not run that day (the library skips the gap) and the
		// next day it runs at 02:30 CEST.
		{"spring forward gap", "30 2 * * *", "Europe/Berlin", "2026-03-28T12:00:00Z", "2026-03-30T00:30:00Z"},
		// Autumn: 02:30 happens twice on 2026-10-25; it fires once.
		{"fall back fires once", "30 2 * * *", "Europe/Berlin", "2026-10-24T12:00:00Z", "2026-10-25T00:30:00Z"},
		{"fall back skips the repeat", "30 2 * * *", "Europe/Berlin", "2026-10-25T00:30:00Z", "2026-10-26T01:30:00Z"},
		{"fall back repeat from a late clock", "30 2 * * *", "Europe/Berlin", "2026-10-25T00:31:00Z", "2026-10-26T01:30:00Z"},
	}
	tests = append(tests, struct{ name, expr, tz, after, want string }{
		"frequent job runs through the repeated hour", "*/15 * * * *", "Europe/Berlin", "2026-10-25T00:45:00Z", "2026-10-25T01:00:00Z"})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Parse(tt.expr, tt.tz)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := s.Next(utc(tt.after))
			if !ok || !got.Equal(utc(tt.want)) {
				t.Errorf("Next(%s) = %v, %v; want %s", tt.after, got, ok, tt.want)
			}
		})
	}
}

func TestNextNever(t *testing.T) {
	s, err := Parse("0 0 31 2 *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); ok {
		t.Errorf("31 February fired at %v", got)
	}
}
