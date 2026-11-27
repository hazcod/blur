package assistant

import (
	"slices"
	"strings"
	"unicode"

	"github.com/hazcod/blur/internal/search"
)

// numberedSubjects retains relationships that an unordered term set loses:
// Route 23 is a different cue from Route 42 followed by a date containing 23.
// Date occurrences and query grammar cannot create these cues. This is bounded
// lexical evidence for ranking, not an extraction of semantic subject IDs.
func numberedSubjects(question string, terms []string) [][2]string {
	ws, dates := eventDateWords(question)
	usable := func(i int) bool {
		return i >= 0 && i < len(ws) && !dates[i] && slices.Contains(terms, search.FoldWord(ws[i].s))
	}
	integer := func(i int) bool {
		return i >= 0 && i < len(ws) && strings.IndexFunc(ws[i].s, func(r rune) bool { return !unicode.IsDigit(r) }) < 0
	}
	compound := func(i int) bool {
		for _, other := range []int{i - 1, i + 1} {
			if !integer(other) {
				continue
			}
			left, right := min(i, other), max(i, other)
			gap := question[ws[left].end:ws[right].start]
			if strings.ContainsAny(gap, ".:/-–") && strings.Trim(gap, " \t\r\n.:/-–") == "" {
				return true // date, clock, range or version component
			}
		}
		return false
	}
	var out [][2]string
	for i, w := range ws {
		if !usable(i) || !integer(i) || compound(i) {
			continue
		}
		label := i - 1
		if !usable(label) || !strings.ContainsFunc(ws[label].s, unicode.IsLetter) {
			label = i + 1
		}
		if !usable(label) || !strings.ContainsFunc(ws[label].s, unicode.IsLetter) {
			continue
		}
		pair := [2]string{search.FoldWord(ws[label].s), w.s}
		if !slices.Contains(out, pair) {
			out = append(out, pair)
		}
	}
	return out
}
