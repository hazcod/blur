package assistant

import (
	"strconv"
	"time"

	"github.com/hazcod/blur/internal/search"
)

// calendarPeriodScope requires an actual period phrase. A numbered topic
// ("project 2025") does not become a capture-time filter.
func calendarPeriodScope(ws []word, now time.Time, allWords []word) (Scope, []string, bool) {
	mk := func(start, end time.Time, label string, used ...string) (Scope, []string, bool) {
		return Scope{Start: start, End: end, Label: label, Explicit: true}, used, true
	}
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	year := time.Date(now.Year(), time.January, 1, 0, 0, 0, 0, now.Location())
	switch {
	case hasSeq(ws, "last", "month"), hasSeq(ws, "previous", "month"):
		return mk(month.AddDate(0, -1, 0), month, "last month")
	case hasSeq(ws, "last", "year"), hasSeq(ws, "previous", "year"):
		return mk(year.AddDate(-1, 0, 0), year, "last year", "year")
	case hasSeq(ws, "this", "year"):
		return mk(year, now, "this year", "year")
	}
	for i := 1; i < len(ws); i++ {
		if !hasWord(ws[i-1:i], "in", "during", "for") {
			continue
		}
		if m, ok := monthWords[ws[i].s]; ok {
			y := now.Year()
			used := []string{ws[i].s}
			if i+1 < len(ws) {
				if n, err := strconv.Atoi(ws[i+1].s); err == nil && len(ws[i+1].s) == 4 && n >= 1 && n <= 9998 {
					y, used = n, append(used, ws[i+1].s)
				} else if ws[i+1].s[0] >= '0' && ws[i+1].s[0] <= '9' {
					// A day/ordinal or another numeric component is not a whole
					// month request; do not coerce it to a month.
					continue
				}
			}
			start := time.Date(y, m, 1, 0, 0, 0, 0, now.Location())
			if len(used) == 1 && start.After(now) {
				start = start.AddDate(-1, 0, 0)
			}
			return mk(start, start.AddDate(0, 1, 0), start.Format("January 2006"), calendarConsumedWords(ws, i, i+len(used), used, allWords)...)
		}
		if n, err := strconv.Atoi(ws[i].s); err == nil && len(ws[i].s) == 4 && n >= 1 && n <= 9998 {
			start := time.Date(n, time.January, 1, 0, 0, 0, 0, now.Location())
			return mk(start, start.AddDate(1, 0, 0), ws[i].s, calendarConsumedWords(ws, i, i+1, []string{ws[i].s}, allWords)...)
		}
	}
	return Scope{}, nil, false
}

// Terms are deduplicated downstream. Consuming one date occurrence must not
// erase a different occurrence in the requested topic (Project 2026, May).
func calendarConsumedWords(ws []word, start, end int, used []string, allWords []word) []string {
	var out []string
	for _, term := range used {
		other := false
		for _, w := range allWords {
			if (w.start < ws[start].start || w.end > ws[end-1].end) && search.FoldWord(w.s) == search.FoldWord(term) {
				other = true
				break
			}
		}
		if !other {
			out = append(out, term)
		}
	}
	return out
}
