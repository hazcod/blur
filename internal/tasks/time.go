package tasks

import (
	"errors"
	"sort"
	"time"
	_ "time/tzdata" // Embed IANA zones for portable scheduling without a system zone database.
)

var (
	ErrNonexistentTime = errors.New("tasks: local time does not exist")
	ErrAmbiguousTime   = errors.New("tasks: local time occurs twice")
	ErrNoOccurrence    = errors.New("tasks: no future occurrence")
)

// ResolveWallTime preserves civil-time intent. time.Date silently normalizes
// gaps and chooses one side of a fold, so enumerate the surrounding offsets
// and require an explicit choice when two instants share the requested clock.
func ResolveWallTime(date, clock, zoneName, fold string) (time.Time, error) {
	if !ValidZone(zoneName) || fold != "" && fold != "first" && fold != "second" {
		return time.Time{}, ErrInvalid
	}
	nominal, err := time.Parse("2006-01-02 15:04", date+" "+clock)
	if err != nil || nominal.Format("2006-01-02") != date || nominal.Format("15:04") != clock || !ValidTime(nominal) {
		return time.Time{}, ErrInvalid
	}
	zone, _ := time.LoadLocation(zoneName)
	offsets := map[int]bool{}
	for hour := -48; hour <= 48; hour++ {
		_, offset := nominal.Add(time.Duration(hour) * time.Hour).In(zone).Zone()
		offsets[offset] = true
	}
	var candidates []time.Time
	for offset := range offsets {
		candidate := nominal.Add(-time.Duration(offset) * time.Second)
		if candidate.In(zone).Format("2006-01-02 15:04") == date+" "+clock {
			candidates = append(candidates, candidate.UTC())
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Before(candidates[j]) })
	if len(candidates) == 0 {
		return time.Time{}, ErrNonexistentTime
	}
	if len(candidates) > 1 && fold == "" {
		return time.Time{}, ErrAmbiguousTime
	}
	if fold == "second" {
		return candidates[len(candidates)-1], nil
	}
	return candidates[0], nil
}

// NextOccurrence advances local calendar dates, not elapsed 24-hour durations.
// Recurrences skip nonexistent local times and use the first instant in a fold.
func NextOccurrence(r Reminder, after time.Time) (time.Time, error) {
	if r.Validate() != nil || !ValidTime(after) {
		return time.Time{}, ErrInvalid
	}
	if r.DueAt.After(after) {
		return r.DueAt.UTC(), nil
	}
	if r.Repeat == Once {
		return time.Time{}, ErrNoOccurrence
	}
	zone, _ := time.LoadLocation(r.Timezone)
	local := after.In(zone)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	clock := time.Date(2000, 1, 1, r.Hour, r.Minute, 0, 0, time.UTC).Format("15:04")
	for count := 0; count < 370; count++ {
		if day.Year() > 2100 {
			break
		}
		if r.Repeat == Daily || day.Weekday() == r.Weekday {
			at, err := ResolveWallTime(day.Format("2006-01-02"), clock, r.Timezone, "first")
			if err != nil && err != ErrNonexistentTime {
				return time.Time{}, err
			}
			if err == nil && at.After(after) && !at.Before(r.DueAt) {
				return at, nil
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return time.Time{}, ErrNoOccurrence
}
