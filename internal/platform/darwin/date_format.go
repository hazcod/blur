package darwin

import "time"

// LocalizedDateTime uses the current macOS region and clock preferences while
// displaying the instant in the requested IANA timezone. Other platforms use a
// deterministic display fallback. Invalid dates/zones return an empty string.
func LocalizedDateTime(at time.Time, zoneName string) string {
	return localizedDateFormat(at, zoneName, false, false)
}

// LocalizedShortDateTime uses the user's short date and clock preferences.
// It is intended for compact scheduled-time labels, where seconds and long
// weekday/month names add noise.
func LocalizedShortDateTime(at time.Time, zoneName string) string {
	return localizedDateFormat(at, zoneName, false, true)
}

// LocalizedTime formats only the clock, with the same preferences as dates.
func LocalizedTime(at time.Time, zoneName string) string {
	return localizedDateFormat(at, zoneName, true, false)
}

func localizedDateFormat(at time.Time, zoneName string, clockOnly, shortDateTime bool) string {
	if at.IsZero() {
		return ""
	}
	if _, err := time.LoadLocation(zoneName); err != nil || zoneName == "" {
		return ""
	}
	if text := nativeDateFormat(at, zoneName, clockOnly, shortDateTime); text != "" {
		return text
	}
	return fallbackDateFormat(at, zoneName, clockOnly, shortDateTime)
}

func fallbackDateFormat(at time.Time, zoneName string, clockOnly, shortDateTime bool) string {
	zone, err := time.LoadLocation(zoneName)
	if err != nil || zoneName == "" || at.IsZero() {
		return ""
	}
	layout := "Mon 2 Jan 2006, 15:04:05"
	if clockOnly {
		layout = "15:04"
	} else if shortDateTime {
		layout = "2 Jan 2006, 15:04"
	}
	return at.In(zone).Format(layout)
}
