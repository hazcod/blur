package reminders

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

const calendarWeekdayPattern = `(?:monday|mon|tuesday|tues|tue|wednesday|weds|wed|thursday|thurs|thur|thu|friday|fri|saturday|sat|sunday|sun)\.?`
const calendarMonthPattern = `(?:january|jan|february|feb|march|mar|april|apr|may|june|jun|july|jul|august|aug|september|sept|sep|october|oct|november|nov|december|dec)\.?`
const namedDatePattern = `(?:` + calendarMonthPattern + ` [0-9]{1,2}(?:st|nd|rd|th)?|[0-9]{1,2}(?:st|nd|rd|th)? ` + calendarMonthPattern + `)`

var weekdayAliases = map[string]string{
	"mon": "monday", "tue": "tuesday", "tues": "tuesday", "wed": "wednesday", "weds": "wednesday",
	"thu": "thursday", "thur": "thursday", "thurs": "thursday", "fri": "friday", "sat": "saturday", "sun": "sunday",
}
var calendarMonths = map[string]time.Month{
	"jan": time.January, "january": time.January, "feb": time.February, "february": time.February,
	"mar": time.March, "march": time.March, "apr": time.April, "april": time.April, "may": time.May,
	"jun": time.June, "june": time.June, "jul": time.July, "july": time.July, "aug": time.August, "august": time.August,
	"sep": time.September, "sept": time.September, "september": time.September, "oct": time.October, "october": time.October,
	"nov": time.November, "november": time.November, "dec": time.December, "december": time.December,
}
var ordinalDay = regexp.MustCompile(`^([0-9]{1,2})(?:st|nd|rd|th)$`)
var punctuatedMeridiem = regexp.MustCompile(`(?i)([ap])\.m\.?`)
var dateBareClock = regexp.MustCompile(`(?i)^(.+?)\s+([0-9]{1,2}:[0-9]{2}|[0-9]{1,2}(?::[0-9]{2})?\s*(?:am|pm)|noon|midnight)$`)

func normalizeCalendarWords(fields []string) {
	for at, word := range fields {
		if at > 2 {
			break
		}
		key := strings.ToLower(strings.TrimSuffix(word, "."))
		if full, ok := weekdayAliases[key]; ok {
			fields[at] = full
		}
	}
}

// calendarDate recognizes unambiguous named-month dates. Numeric slash dates
// stay unresolved because day/month order depends on locale. An omitted year
// means the next occurrence, but an explicit year is never silently moved.
func calendarDate(spec string, local time.Time) (time.Time, bool) {
	spec = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(spec, "on "), "for "))
	if parsed, err := time.Parse("2006-01-02", spec); err == nil {
		return parsed, true
	}
	fields := strings.Fields(strings.ReplaceAll(spec, ",", ""))
	if len(fields) < 2 || len(fields) > 3 {
		return time.Time{}, false
	}
	month, found := calendarMonths[strings.TrimSuffix(fields[0], ".")]
	dayWord := fields[1]
	if !found {
		month, found = calendarMonths[strings.TrimSuffix(fields[1], ".")]
		dayWord = fields[0]
	}
	if !found {
		return time.Time{}, false
	}
	if ordinal := ordinalDay.FindStringSubmatch(dayWord); ordinal != nil {
		dayWord = ordinal[1]
	}
	day, err := strconv.Atoi(dayWord)
	if err != nil || day < 1 || day > 31 {
		return time.Time{}, false
	}
	year := local.Year()
	if len(fields) == 3 {
		if len(fields[2]) != 4 {
			return time.Time{}, false
		}
		year, err = strconv.Atoi(fields[2])
		if err != nil || year < 1970 || year > 9999 {
			return time.Time{}, false
		}
	}
	date := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	if date.Year() != year || date.Month() != month || date.Day() != day {
		return time.Time{}, false
	}
	if len(fields) == 2 && date.Before(time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)) {
		date = date.AddDate(1, 0, 0)
		if date.Month() != month || date.Day() != day {
			return time.Time{}, false
		}
	}
	return date, true
}
