// Package reminders manages explicit schedules and native notification delivery.
package reminders

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/tasks"
)

type Draft struct {
	Title    string
	Reminder *tasks.Reminder
	Missing  string
	When     string
	Anchor   time.Time
	Timezone string
}

var relativePattern = regexp.MustCompile(`(?i)^(?:in ?)?([0-9]{1,6}) ?(s|secs?|seconds?|m|mins?|minutes?|h|hrs?|hours?|d|days?|weeks?)$`)
var titleSchedulePattern = regexp.MustCompile(`(?i)^(.+?)\s+((?:in ?[0-9a-z-]+ ?[a-z]+)|(?:(?:day after tomorrow|today|tomorrow|tonight|` + calendarWeekdayPattern + `|` + namedDatePattern + `)(?: .+)?)|(?:(?:on|for|next|this|every|at) .+))$`)
var clockPattern = regexp.MustCompile(`(?i)^([0-9]{1,2})(?::([0-9]{2}))?\s*(am|pm)?$`)
var weekdays = map[string]time.Weekday{"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday, "thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday}

// Parse handles explicit, deterministic schedule forms. Unclear essential
// fields are returned for clarification. Date-only and daypart requests use
// the app's authorized local-time defaults; explicit clocks and DST ambiguity
// are never silently changed.
func Parse(input string, now time.Time, zoneName string) (Draft, error) {
	if len(input) > 2048 || !tasks.ValidTime(now) {
		return Draft{}, tasks.ErrInvalid
	}
	body := strings.TrimSpace(input)
	lower := strings.ToLower(body)
	switch {
	case strings.HasPrefix(lower, "reminder:"):
		body = strings.TrimSpace(body[len("reminder:"):])
	case lower == "remind me":
		body = ""
	case strings.HasPrefix(lower, "remind me "):
		body = strings.TrimSpace(body[len("remind me "):])
	default:
		return Draft{}, tasks.ErrInvalid
	}
	var title, spec string
	if strings.HasPrefix(strings.ToLower(body), "about ") {
		body = "to " + strings.TrimSpace(body[6:])
	}
	if strings.HasPrefix(strings.ToLower(body), "to ") {
		title = strings.TrimSpace(body[3:])
		if match := titleSchedulePattern.FindStringSubmatch(title); match != nil {
			title, spec = strings.TrimSpace(match[1]), strings.TrimSpace(match[2])
		}
	} else if index := strings.Index(strings.ToLower(body), " to "); index >= 0 {
		spec, title = strings.TrimSpace(body[:index]), strings.TrimSpace(body[index+4:])
	} else {
		spec = body
	}
	spec = NormalizeSchedule(spec)
	draft := Draft{Title: title, When: spec, Anchor: now, Timezone: zoneName}
	if title != "" && !tasks.ValidTitle(title) {
		return Draft{}, tasks.ErrInvalid
	}
	if spec == "" {
		draft.Missing = "time"
		if title == "" {
			draft.Missing = "title"
		}
		return draft, nil
	}
	// A named IANA zone can be supplied after the clock without changing the
	// caller's default timezone for later commands.
	fields := strings.Fields(spec)
	if len(fields) > 0 {
		last := fields[len(fields)-1]
		if strings.Contains(last, "/") || last == "UTC" {
			if !tasks.ValidZone(last) {
				draft.Missing = "timezone"
				return draft, nil
			}
			zoneName = last
			draft.Timezone = zoneName
			spec = strings.TrimSpace(strings.TrimSuffix(spec, last))
		}
	}
	if !tasks.ValidZone(zoneName) {
		draft.Missing = "timezone"
		return draft, nil
	}
	zone, _ := time.LoadLocation(zoneName)
	local := now.In(zone)
	absoluteSpec := spec
	if strings.HasPrefix(strings.ToLower(absoluteSpec), "at ") {
		absoluteSpec = strings.TrimSpace(absoluteSpec[3:])
	}
	absolute, absoluteErr := time.Parse(time.RFC3339Nano, absoluteSpec)
	if absoluteErr == nil {
		if !tasks.ValidTime(absolute) || !absolute.After(now) {
			draft.Missing = "future_time"
			return draft, nil
		}
		draft.Reminder = &tasks.Reminder{DueAt: absolute.UTC(), Timezone: zoneName}
	} else if match := relativePattern.FindStringSubmatch(spec); match != nil {
		number, _ := strconv.Atoi(match[1])
		if number < 1 {
			draft.Missing = "future_time"
			return draft, nil
		}
		unit := strings.ToLower(strings.TrimSuffix(match[2], "s"))
		var due time.Time
		switch unit {
		case "", "sec", "second":
			due = now.Add(time.Duration(number) * time.Second)
		case "m", "min", "minute":
			due = now.Add(time.Duration(number) * time.Minute)
		case "h", "hr", "hour":
			due = now.Add(time.Duration(number) * time.Hour)
		case "d", "day", "week":
			if unit == "week" {
				number *= 7
			}
			day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, number)
			var err error
			due, err = tasks.ResolveWallTime(day.Format("2006-01-02"), local.Format("15:04"), zoneName, "")
			if err != nil {
				draft.Missing = timeProblem(err)
				return draft, nil
			}
			due = due.Add(time.Duration(local.Second())*time.Second + time.Duration(local.Nanosecond()))
		}
		if !tasks.ValidTime(due) {
			draft.Missing = "future_time"
			return draft, nil
		}
		draft.Reminder = &tasks.Reminder{DueAt: due.UTC(), Timezone: zoneName}
	} else {
		spec = strings.ToLower(strings.TrimSpace(spec))
		foldChoice := ""
		for _, choice := range []string{"first", "second"} {
			if strings.HasSuffix(spec, " "+choice) {
				foldChoice = choice
				spec = strings.TrimSpace(strings.TrimSuffix(spec, " "+choice))
				break
			}
		}
		repeat := tasks.Once
		weekday := time.Sunday
		prefix, clock := spec, ""
		explicitClock := false
		if strings.HasPrefix(spec, "at ") {
			prefix = ""
			clock = strings.TrimSpace(spec[3:])
			explicitClock = true
		} else if at := strings.LastIndex(spec, " at "); at >= 0 {
			prefix, clock = strings.TrimSpace(spec[:at]), strings.TrimSpace(spec[at+4:])
			explicitClock = true
		}
		prefix, defaultClock := calendarDefault(prefix)
		if !explicitClock {
			clock = defaultClock
		}
		hour, minute, ok := parseClock(clock)
		if !ok {
			draft.Missing = "time"
			return draft, nil
		}
		clock = time.Date(2000, 1, 1, hour, minute, 0, 0, time.UTC).Format("15:04")
		day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
		explicitDate := false
		switch {
		case prefix == "":
		case prefix == "today" || prefix == "tonight":
			explicitDate = true
		case prefix == "tomorrow":
			day = day.AddDate(0, 0, 1)
			explicitDate = true
		case prefix == "day after tomorrow":
			day = day.AddDate(0, 0, 2)
			explicitDate = true
		case strings.HasPrefix(prefix, "on "):
			parsed, ok := calendarDate(prefix, local)
			if !ok {
				draft.Missing = "date"
				return draft, nil
			}
			day = parsed
			explicitDate = true
		case strings.HasPrefix(prefix, "this "):
			word := strings.TrimSpace(strings.TrimPrefix(prefix, "this "))
			var found bool
			weekday, found = weekdays[word]
			if !found {
				draft.Missing = "date"
				return draft, nil
			}
			// "This" names a date in the current local Monday-Sunday week,
			// including a past date that must be clarified rather than moved.
			delta := (int(weekday)+6)%7 - (int(day.Weekday())+6)%7
			day = day.AddDate(0, 0, delta)
			explicitDate = true
		case prefix == "every day" || prefix == "everyday" || prefix == "daily":
			repeat = tasks.Daily
		case strings.HasPrefix(prefix, "every ") || strings.HasPrefix(prefix, "next "):
			word := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(prefix, "every "), "next "))
			var found bool
			weekday, found = weekdays[word]
			if !found {
				draft.Missing = "date"
				return draft, nil
			}
			delta := (int(weekday) - int(day.Weekday()) + 7) % 7
			if strings.HasPrefix(prefix, "next ") && delta == 0 {
				delta = 7
			}
			day = day.AddDate(0, 0, delta)
			if strings.HasPrefix(prefix, "every ") {
				repeat = tasks.Weekly
			} else {
				explicitDate = true
			}
		default:
			if named, found := weekdays[prefix]; found {
				weekday = named
				delta := (int(weekday) - int(day.Weekday()) + 7) % 7
				day, explicitDate = day.AddDate(0, 0, delta), true
				break
			}
			parsed, ok := calendarDate(prefix, local)
			if !ok {
				draft.Missing = "date"
				return draft, nil
			}
			day, explicitDate = parsed, true
		}
		var due time.Time
		if repeat != tasks.Once && foldChoice == "second" {
			draft.Missing = "time"
			return draft, nil
		}
		for attempts := 0; attempts < 15; attempts++ {
			fold := foldChoice
			if repeat != tasks.Once {
				fold = "first"
			}
			var err error
			due, err = tasks.ResolveWallTime(day.Format("2006-01-02"), clock, zoneName, fold)
			if err != nil {
				if repeat != tasks.Once && err == tasks.ErrNonexistentTime {
					step := 1
					if repeat == tasks.Weekly {
						step = 7
					}
					day = day.AddDate(0, 0, step)
					continue
				}
				draft.Missing = timeProblem(err)
				return draft, nil
			}
			if due.After(now) {
				break
			}
			if explicitDate {
				if !explicitClock && day.Format("2006-01-02") == local.Format("2006-01-02") {
					if next, ok := nextSameDayDefault(now, zoneName); ok {
						due = next
						break
					}
				}
				draft.Missing = "future_time"
				return draft, nil
			}
			step := 1
			if repeat == tasks.Weekly {
				step = 7
			}
			day = day.AddDate(0, 0, step)
		}
		if !due.After(now) {
			draft.Missing = "future_time"
			return draft, nil
		}
		draft.Reminder = &tasks.Reminder{DueAt: due.UTC(), Timezone: zoneName, Repeat: repeat}
		if repeat != tasks.Once {
			draft.Reminder.Hour = hour
			draft.Reminder.Minute = minute
			draft.Reminder.Weekday = weekday
		}
	}
	if title == "" {
		draft.Missing = "title"
	}
	return draft, nil
}

func parseClock(clock string) (int, int, bool) {
	switch strings.ToLower(strings.TrimSpace(clock)) {
	case "noon":
		return 12, 0, true
	case "midnight":
		return 0, 0, true
	}
	match := clockPattern.FindStringSubmatch(clock)
	if match == nil {
		return 0, 0, false
	}
	hour, _ := strconv.Atoi(match[1])
	minute := 0
	if match[2] != "" {
		minute, _ = strconv.Atoi(match[2])
	}
	if minute > 59 {
		return 0, 0, false
	}
	if match[3] != "" {
		if hour < 1 || hour > 12 {
			return 0, 0, false
		}
		hour %= 12
		if strings.EqualFold(match[3], "pm") {
			hour += 12
		}
	} else if hour > 23 || match[2] == "" && hour > 0 && hour <= 12 {
		return 0, 0, false
	}
	return hour, minute, true
}
func timeProblem(err error) string {
	switch err {
	case tasks.ErrNonexistentTime:
		return "nonexistent_time"
	case tasks.ErrAmbiguousTime:
		return "ambiguous_time"
	default:
		return "time"
	}
}
