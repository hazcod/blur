package assistant

import (
	"strconv"
	"strings"
	"time"
)

// calendarDayScope recognizes observation dates after event-date and literal
// masking. A full date takes precedence over a bare weekday or month period.
func calendarDayScope(ws []word, now time.Time, allWords []word) (Scope, []string, bool) {
	for i, w := range ws {
		month, ok := monthWords[w.s]
		if !ok {
			continue
		}
		start, end, dayWord := i, i+1, -1
		if i > 0 {
			if _, ok := calendarDayNumber(ws[i-1].s); ok {
				start, dayWord = i-1, i-1
			}
		}
		if dayWord < 0 && i+1 < len(ws) {
			if _, ok := calendarDayNumber(ws[i+1].s); ok {
				end, dayWord = i+2, i+1
			}
		}
		if dayWord < 0 {
			continue
		}
		day, _ := calendarDayNumber(ws[dayWord].s)
		year, explicitYear := now.Year(), false
		if end < len(ws) {
			if n, err := strconv.Atoi(ws[end].s); err == nil {
				if len(ws[end].s) != 4 || n < 1 || n > 9998 {
					continue
				}
				year, explicitYear, end = n, true, end+1
			}
		}
		if start > 0 {
			if _, weekday := weekdayWords[ws[start-1].s]; weekday {
				start--
			}
		}
		// Without a year, require a day phrase. In particular, a numbered
		// subject in "invoice 23 in November 23" is not a capture date.
		if !explicitYear && (start == 0 || !hasWord(ws[start-1:start], "on", "from", "during")) {
			continue
		}
		date := time.Date(year, month, day, 0, 0, 0, 0, now.Location())
		if !explicitYear && date.After(now) {
			year--
			date = time.Date(year, month, day, 0, 0, 0, 0, now.Location())
		}
		// time.Date normalizes invalid days, which must never widen a query.
		if date.Year() != year || date.Month() != month || date.Day() != day {
			continue
		}
		scope := Scope{Start: date, End: date.AddDate(0, 0, 1), Label: date.Format("2 January 2006"), Explicit: true}
		if part, partStart, partEnd := calendarDayPart(ws, start, end); part != "" {
			hours := dayPartHours[part]
			scope.Start = time.Date(year, month, day, hours[0], 0, 0, 0, now.Location())
			scope.End = time.Date(year, month, day, hours[1], 0, 0, 0, now.Location())
			if !explicitYear {
				scope.Label = date.Format("2 January")
			}
			scope.Label += " " + part
			start, end = partStart, partEnd
		}
		used := make([]string, 0, end-start)
		for _, term := range ws[start:end] {
			used = append(used, term.s)
		}
		return scope, calendarConsumedWords(ws, start, end, used, allWords), true
	}
	return Scope{}, nil, false
}

func calendarDayNumber(s string) (int, bool) {
	for _, suffix := range []string{"st", "nd", "rd", "th"} {
		s = strings.TrimSuffix(s, suffix)
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 1 && n <= 31
}

// calendarDayPart keeps an adjacent daypart attached across the date's year:
// "1 November 2025 morning" and "the morning of November 1 2025".
func calendarDayPart(ws []word, start, end int) (string, int, int) {
	for i := end; i < len(ws); i++ {
		if _, part := dayPartHours[ws[i].s]; part {
			return ws[i].s, start, i + 1
		}
		if _, filler := dayPartFillers[ws[i].s]; !filler {
			break
		}
	}
	for i := start - 1; i >= 0; i-- {
		if _, part := dayPartHours[ws[i].s]; part {
			return ws[i].s, i, end
		}
		if _, filler := dayPartFillers[ws[i].s]; !filler {
			break
		}
	}
	return "", start, end
}
