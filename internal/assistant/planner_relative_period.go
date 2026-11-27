package assistant

import (
	"errors"
	"strconv"
	"time"
)

func relativePeriodScope(ws []word, now time.Time, allWords []word) (Scope, []string, bool) {
	for i := 0; i+2 < len(ws); i++ {
		if ws[i].s != "last" && ws[i].s != "past" {
			continue
		}
		unit := parseUnit(ws[i+2].s)
		if unit == 0 {
			continue
		}
		n, err := strconv.ParseUint(ws[i+1].s, 10, 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) || n == 0 {
			continue
		}
		start := relativePeriodStart(now, n, unit)
		used := calendarConsumedWords(ws, i+1, i+3, []string{ws[i+1].s, ws[i+2].s}, allWords)
		return Scope{Start: start, End: now, Label: "last " + ws[i+1].s + " " + ws[i+2].s, Explicit: true}, used, true
	}
	return Scope{}, nil, false
}

// Search has no fixed historical horizon. The authenticated timestamp codec
// uses int64 Unix nanoseconds; requests reaching further back include its entire
// representable history. Bound arithmetic before conversion to avoid duration
// overflow accidentally turning a large request into a recent window.
func relativePeriodStart(now time.Time, n uint64, unit byte) time.Time {
	first := time.Unix(0, -1<<63).In(now.Location())
	var start time.Time
	switch unit {
	case 'h', 'm':
		seconds := int64(60)
		if unit == 'h' {
			seconds = 3600
		}
		if n > uint64((now.Unix()-first.Unix())/seconds) {
			return first
		}
		start = time.Unix(now.Unix()-int64(n)*seconds, int64(now.Nanosecond())).In(now.Location())
	case 'd', 'w':
		multiplier := uint64(1)
		if unit == 'w' {
			multiplier = 7
		}
		if n > uint64(now.Year()*366)/multiplier {
			return first
		}
		start = now.AddDate(0, 0, -int(n*multiplier))
	case 'M':
		if n > uint64(now.Year()*12) {
			return first
		}
		start = now.AddDate(0, -int(n), 0)
	case 'y':
		if n >= uint64(now.Year()) {
			return first
		}
		start = now.AddDate(-int(n), 0, 0)
	}
	if start.Before(first) {
		return first
	}
	return start
}
