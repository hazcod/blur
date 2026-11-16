package reminders

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/tasks"
)

var spokenNumber = map[string]int{"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "fifteen": 15, "twenty": 20, "thirty": 30, "forty": 40, "forty-five": 45, "sixty": 60}
var bareDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(?:\s|$)`)

// NormalizeSchedule converts equivalent explicit words only. Calendar default
// times are resolved by Parse, never inserted into the original scheduling span.
func NormalizeSchedule(spec string) string {
	spec = strings.TrimSpace(spec)
	spec = punctuatedMeridiem.ReplaceAllString(spec, "${1}m")
	fields := strings.Fields(spec)
	for n, word := range fields {
		if value, ok := spokenNumber[strings.ToLower(word)]; ok {
			fields[n] = strconv.Itoa(value)
		} else if strings.EqualFold(word, "UTC") {
			fields[n] = "UTC"
		}
	}
	normalizeCalendarWords(fields)
	spec = strings.Join(fields, " ")
	if match := relativePattern.FindStringSubmatch(spec); match != nil {
		return "in " + match[1] + " " + strings.ToLower(match[2])
	}
	lower := strings.ToLower(spec)
	for _, prefix := range []string{"on ", "for "} {
		if strings.HasPrefix(lower, prefix) {
			rest := strings.TrimSpace(spec[len(prefix):])
			parts := strings.Fields(rest)
			if len(parts) > 0 {
				if _, ok := weekdays[strings.ToLower(parts[0])]; ok {
					spec = rest
					fields = strings.Fields(spec)
					lower = strings.ToLower(spec)
				} else if strings.EqualFold(parts[0], "this") || strings.EqualFold(parts[0], "next") || strings.EqualFold(parts[0], "every") {
					spec = rest
					fields = strings.Fields(spec)
					lower = strings.ToLower(spec)
				}
			}
		}
	}
	if len(fields) > 0 {
		if bareDate.MatchString(lower) {
			spec = "on " + spec
		}
	}
	zoneSuffix := ""
	fields = strings.Fields(spec)
	if len(fields) > 1 {
		last := fields[len(fields)-1]
		if strings.Contains(last, "/") || last == "UTC" {
			zoneSuffix = " " + last
			spec = strings.TrimSpace(strings.TrimSuffix(spec, last))
		}
	}
	if _, _, ok := parseClock(spec); ok {
		spec = "at " + spec
	} else if !strings.Contains(strings.ToLower(spec), " at ") && !strings.HasPrefix(strings.ToLower(spec), "at ") {
		if match := dateBareClock.FindStringSubmatch(spec); match != nil {
			spec = match[1] + " at " + match[2]
		}
	}
	return spec + zoneSuffix
}

func Continue(previous Draft, reply string, now time.Time, zone string) (Draft, error) {
	if !tasks.ValidTime(now) || len(reply) > 2048 {
		return Draft{}, tasks.ErrInvalid
	}
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return previous, nil
	}
	if previous.Timezone != "" && tasks.ValidZone(previous.Timezone) {
		zone = previous.Timezone
	}
	anchor := previous.Anchor
	if !tasks.ValidTime(anchor) {
		anchor = now
	}
	if previous.Missing == "title" {
		literal := strings.HasPrefix(strings.ToLower(reply), "to ")
		if !literal {
			spec := NormalizeSchedule(reply)
			lower := strings.ToLower(spec)
			schedule := lower == "today" || lower == "tomorrow" || lower == "daily"
			for _, prefix := range []string{"at ", "in ", "next ", "on ", "every ", "today at ", "tomorrow at "} {
				if strings.HasPrefix(lower, prefix) {
					schedule = true
				}
			}
			if schedule {
				return Parse("remind me "+spec, now, zone)
			}
		}
		title := reply
		if literal {
			title = strings.TrimSpace(reply[3:])
		}
		if !tasks.ValidTitle(title) {
			return Draft{}, tasks.ErrInvalid
		}
		if previous.Reminder != nil {
			copy := *previous.Reminder
			previous.Reminder = &copy
			previous.Title, previous.Missing = title, ""
			if !copy.DueAt.After(now) {
				previous.Missing = "future_time"
			}
			return previous, nil
		}
		return Parse("remind me "+previous.When+" to "+title, anchor, zone)
	}
	spec := NormalizeSchedule(reply)
	merged := false
	switch previous.Missing {
	case "time", "future_time", "":
		if strings.HasPrefix(strings.ToLower(spec), "at ") && previous.When != "" {
			date := previous.When
			// The draft already retains its explicit timezone. Leave the date
			// text free of a trailing zone before appending the user's clock;
			// Parse recognizes named zones only at the end of a schedule.
			fields := strings.Fields(date)
			if len(fields) > 0 && fields[len(fields)-1] == previous.Timezone {
				date = strings.TrimSpace(strings.TrimSuffix(date, previous.Timezone))
			}
			if at := strings.LastIndex(strings.ToLower(date), " at "); at >= 0 {
				date = strings.TrimSpace(date[:at])
			} else if strings.HasPrefix(strings.ToLower(date), "at ") {
				date = ""
			}
			for _, daypart := range []string{" morning", " afternoon", " evening", " night"} {
				if strings.HasSuffix(strings.ToLower(date), daypart) {
					date = strings.TrimSpace(date[:len(date)-len(daypart)])
					break
				}
			}
			if date != "" {
				spec = date + " " + spec
			}
			merged = true
		}
	case "date":
		if at := strings.LastIndex(strings.ToLower(previous.When), " at "); at >= 0 && !strings.Contains(strings.ToLower(spec), " at ") {
			spec += previous.When[at:]
			merged = true
		}
	case "ambiguous_time":
		if strings.EqualFold(reply, "first") || strings.EqualFold(reply, "second") {
			spec = previous.When + " " + strings.ToLower(reply)
			merged = true
		}
	case "timezone":
		if !tasks.ValidZone(reply) {
			return previous, nil
		}
		fields := strings.Fields(previous.When)
		if len(fields) > 0 && strings.Contains(fields[len(fields)-1], "/") {
			fields = fields[:len(fields)-1]
		}
		spec = strings.Join(fields, " ") + " " + reply
		merged = true
	}
	if !merged {
		anchor = now
	}
	draft, err := Parse("remind me "+spec+" to "+previous.Title, anchor, zone)
	if err == nil && draft.Reminder != nil && !draft.Reminder.DueAt.After(now) {
		draft.Missing = "future_time"
	}
	return draft, err
}
