package reminders

import (
	"strings"
	"time"

	"github.com/hazcod/blur/internal/tasks"
)

// Calendar defaults are product policy, not model-supplied timing. Date-only
// requests use 09:00; morning/afternoon/evening use 09:00/14:00/18:00 and
// tonight/night use 20:00. An explicit clock always takes precedence.
func calendarDefault(prefix string) (string, string) {
	if prefix == "" {
		return "", ""
	}
	clock := "09:00"
	if prefix == "tonight" {
		return prefix, "20:00"
	}
	for _, part := range []struct{ name, clock string }{{"morning", "09:00"}, {"afternoon", "14:00"}, {"evening", "18:00"}, {"night", "20:00"}} {
		if prefix == part.name || prefix == "this "+part.name {
			return "today", part.clock
		}
		if strings.HasSuffix(prefix, " "+part.name) {
			return strings.TrimSpace(strings.TrimSuffix(prefix, " "+part.name)), part.clock
		}
	}
	return prefix, clock
}

// A passed default on an explicitly named current day moves to the next full
// local hour. In the last hour, use now+5m only if it remains on that civil day.
// ResolveWallTime rejects skipped/repeated hours rather than guessing a fold.
func nextSameDayDefault(now time.Time, zoneName string) (time.Time, bool) {
	zone, err := time.LoadLocation(zoneName)
	if err != nil {
		return time.Time{}, false
	}
	local := now.In(zone)
	date := local.Format("2006-01-02")
	if local.Hour() < 23 {
		clock := time.Date(2000, 1, 1, local.Hour()+1, 0, 0, 0, time.UTC).Format("15:04")
		if next, err := tasks.ResolveWallTime(date, clock, zoneName, ""); err == nil && next.After(now) {
			return next, true
		}
	}
	next := now.Add(5 * time.Minute)
	return next, next.In(zone).Format("2006-01-02") == date
}
