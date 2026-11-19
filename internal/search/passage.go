package search

import (
	"unicode"
	"unicode/utf8"
)

// MatchPassage locates a bounded passage containing the most distinct exact
// query terms, with nearby context. Offsets refer to the unchanged source.
// It scans the saved text locally; the original remains available for reading.
// Zero End means there was no text match (for example, only app metadata matched).
func MatchPassage(text string, terms []string, maxBytes int) (start, end int) {
	if maxBytes < 1 || len(terms) == 0 || len(terms) > MaxTerms || !utf8.ValidString(text) {
		return 0, 0
	}
	wanted := make(map[string]int, len(terms))
	for _, term := range terms {
		// Both sides are compared in the index's folded space; terms as given
		// may still be accented or capitalised. Offsets stay in the original.
		term = FoldWord(term)
		if _, seen := wanted[term]; !seen {
			wanted[term] = len(wanted)
		}
	}
	type occurrence struct{ start, end, term int }
	var matches []occurrence
	tokenStart := -1
	flush := func(offset int) {
		if tokenStart < 0 {
			return
		}
		if index, found := wanted[FoldWord(text[tokenStart:offset])]; found {
			matches = append(matches, occurrence{tokenStart, offset, index})
		}
		tokenStart = -1
	}
	for offset, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || tokenStart >= 0 && IsCombiningAccent(r) {
			if tokenStart < 0 {
				tokenStart = offset
			}
		} else {
			flush(offset)
		}
	}
	flush(len(text))
	if len(matches) == 0 {
		return 0, 0
	}
	if len(text) <= maxBytes {
		return 0, len(text)
	}
	counts := make([]int, len(wanted))
	left, distinct, bestDistinct, bestCount := 0, 0, 0, 0
	bestStart, bestEnd := matches[0].start, matches[0].end
	for right, match := range matches {
		if counts[match.term] == 0 {
			distinct++
		}
		counts[match.term]++
		for left < right && match.end-matches[left].start > maxBytes {
			counts[matches[left].term]--
			if counts[matches[left].term] == 0 {
				distinct--
			}
			left++
		}
		count := right - left + 1
		if distinct > bestDistinct || distinct == bestDistinct && count > bestCount {
			bestDistinct, bestCount = distinct, count
			bestStart, bestEnd = matches[left].start, match.end
		}
	}
	start = max(0, bestStart-maxBytes/4)
	if bestEnd > start+maxBytes {
		start = bestEnd - maxBytes
	}
	end = min(len(text), start+maxBytes)
	if end == len(text) {
		start = max(0, end-maxBytes)
	}
	for start < end && !utf8.RuneStart(text[start]) {
		start++
	}
	for end > start && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	if end <= start {
		return 0, 0
	}
	return start, end
}
