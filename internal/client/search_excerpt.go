package client

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/search"
)

const (
	// searchExcerptBytes is the preview size of a search row on both surfaces.
	searchExcerptBytes = 240
	// minSearchExcerptBytes is the smallest preview a byte budget may shrink to.
	minSearchExcerptBytes = 80
	// excerptBoundaryReach is how far a cut edge may move to land on a
	// sentence boundary.
	excerptBoundaryReach = 60
	excerptWordReach     = 12
	excerptEllipsis      = "…"
)

// Excerpt returns a short preview of text centred on the span
// [matchStart, matchEnd). The result is at most maxBytes bytes, valid UTF-8,
// has runs of whitespace collapsed, prefers to open and close on a sentence
// boundary when one is within 60 bytes of a cut, and marks any other cut edge
// with an ellipsis. The original text is never modified; previews are not
// evidence and callers must open the saved capture before quoting.
func Excerpt(text string, matchStart, matchEnd, maxBytes int) string {
	out, _, _ := ExcerptSpan(text, matchStart, matchEnd, maxBytes)
	return out
}

// ExcerptSpan is Excerpt plus the byte offsets of the matched span inside the
// returned preview, so a surface can emphasise it.
func ExcerptSpan(text string, matchStart, matchEnd, maxBytes int) (string, int, int) {
	if maxBytes < 1 || text == "" {
		return "", 0, 0
	}
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	matchStart = min(max(matchStart, 0), len(text))
	matchEnd = min(max(matchEnd, matchStart), len(text))
	for matchStart > 0 && matchStart < len(text) && !utf8.RuneStart(text[matchStart]) {
		matchStart--
	}
	for matchEnd < len(text) && !utf8.RuneStart(text[matchEnd]) {
		matchEnd++
	}
	start, end := 0, len(text)
	if len(text) > maxBytes {
		start, end = excerptWindow(text, matchStart, matchEnd, maxBytes)
	}
	leftCut, rightCut := start > 0, end < len(text)
	if start > 0 || end < len(text) {
		start, end, leftCut, rightCut = snapExcerptEdges(text, start, end, matchStart, matchEnd)
	}
	body, from, to := collapseExcerpt(text[start:end], matchStart-start, matchEnd-start)
	if leftCut {
		body, from, to = excerptEllipsis+body, from+len(excerptEllipsis), to+len(excerptEllipsis)
	}
	if rightCut {
		body += excerptEllipsis
	}
	return body, from, to
}

// excerptWindow chooses the byte window [start, end) of at most maxBytes that
// centres the match, reserving room for an ellipsis on every cut edge.
func excerptWindow(text string, matchStart, matchEnd, maxBytes int) (int, int) {
	reserve := 0
	var start, end int
	for range 3 {
		budget := maxBytes - reserve
		if budget < 1 {
			budget = maxBytes
		}
		start, end = centredWindow(text, matchStart, matchEnd, budget)
		needed := 0
		if start > 0 {
			needed += len(excerptEllipsis)
		}
		if end < len(text) {
			needed += len(excerptEllipsis)
		}
		if needed <= reserve || maxBytes-needed < 1 {
			break
		}
		reserve = needed
	}
	return start, end
}

func centredWindow(text string, matchStart, matchEnd, budget int) (int, int) {
	var start, end int
	if matchEnd-matchStart >= budget {
		start, end = matchStart, matchStart+budget
	} else {
		start = matchStart - (budget-(matchEnd-matchStart))/2
		end = start + budget
	}
	if start < 0 {
		start, end = 0, min(budget, len(text))
	}
	if end > len(text) {
		end = len(text)
		start = max(0, end-budget)
	}
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	for end > start && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	return start, end
}

func isSentenceEnd(text string, i int) bool {
	switch text[i] {
	case '\n':
		return true
	case '.', '!', '?':
		return i+1 == len(text) || text[i+1] == ' ' || text[i+1] == '\n' || text[i+1] == '\t' || text[i+1] == '\r'
	}
	return false
}

// snapExcerptEdges moves each cut edge onto a sentence boundary within reach,
// else onto a word boundary, without ever excluding the match. It reports
// which edges still cut text mid-sentence and so need an ellipsis.
func snapExcerptEdges(text string, start, end, matchStart, matchEnd int) (int, int, bool, bool) {
	leftCut, rightCut := start > 0, end < len(text)
	if leftCut {
		snapped := false
		for i := start; i < min(start+excerptBoundaryReach, matchStart); i++ {
			if isSentenceEnd(text, i) {
				next := i + 1
				for next < len(text) && (text[next] == ' ' || text[next] == '\n' || text[next] == '\t' || text[next] == '\r') {
					next++
				}
				if next <= matchStart {
					start, snapped = next, true
					break
				}
			}
		}
		if snapped {
			leftCut = false
		} else if start > 0 && !isBreakByte(text[start-1]) && !isBreakByte(text[start]) {
			for i := start; i < min(start+excerptWordReach, matchStart); i++ {
				if isBreakByte(text[i]) {
					start = i + 1
					break
				}
			}
		}
	}
	if rightCut {
		snapped := false
		for i := end - 1; i >= max(end-excerptBoundaryReach, matchEnd); i-- {
			if isSentenceEnd(text, i) {
				if text[i] == '\n' {
					end = i
				} else {
					end = i + 1
				}
				snapped = true
				break
			}
		}
		if snapped {
			rightCut = false
		} else if end < len(text) && !isBreakByte(text[end-1]) && !isBreakByte(text[end]) {
			for i := end - 1; i > max(end-excerptWordReach, matchEnd); i-- {
				if isBreakByte(text[i]) {
					end = i
					break
				}
			}
		}
	}
	if end < start {
		end = start
	}
	return start, end, leftCut, rightCut
}

func isBreakByte(b byte) bool { return b == ' ' || b == '\n' || b == '\t' || b == '\r' }

// collapseExcerpt trims the window, collapses whitespace runs into one space
// and maps the matched span into the collapsed text.
func collapseExcerpt(window string, from, to int) (string, int, int) {
	var b strings.Builder
	b.Grow(len(window))
	pending := false
	outStart, outEnd := -1, -1
	for offset, r := range window {
		if unicode.IsSpace(r) || r == 0 {
			pending = b.Len() > 0
			continue
		}
		if pending {
			b.WriteByte(' ')
			pending = false
		}
		inMatch := offset >= from && offset < to
		if inMatch && outStart < 0 {
			outStart = b.Len()
		}
		b.WriteRune(r)
		if inMatch {
			outEnd = b.Len()
		}
	}
	if outStart < 0 {
		return b.String(), 0, 0
	}
	return b.String(), outStart, outEnd
}

// QueryExcerpt previews text around the densest cluster of query terms.
// semanticStart/semanticEnd is a passage chosen by meaning, used only when no
// term occurs in the text. Without any match the preview starts at the head.
func QueryExcerpt(text string, terms []string, semanticStart, semanticEnd, maxBytes int) string {
	out, _, _ := QueryExcerptSpan(text, terms, semanticStart, semanticEnd, maxBytes)
	return out
}

// QueryExcerptSpan is QueryExcerpt plus the matched span inside the preview.
func QueryExcerptSpan(text string, terms []string, semanticStart, semanticEnd, maxBytes int) (string, int, int) {
	start, end := queryMatchSpan(text, terms, semanticStart, semanticEnd, maxBytes)
	return ExcerptSpan(text, start, end, maxBytes)
}

// queryMatchSpan finds the densest cluster of query terms, or the semantic
// passage when no term occurs. The result is the span an excerpt centres on;
// zero means there is no match and the excerpt starts at the head.
func queryMatchSpan(text string, terms []string, semanticStart, semanticEnd, maxBytes int) (int, int) {
	start, end := 0, 0
	if len(terms) > 0 && len(terms) <= search.MaxTerms {
		start, end = search.MatchPassage(text, terms, max(1, maxBytes/2))
		if start == 0 && end >= len(text) {
			start, end = 0, 0 // the whole text is the passage: nothing to centre on
		}
	}
	if end <= start && semanticEnd > semanticStart && semanticStart >= 0 && semanticEnd <= len(text) {
		start, end = semanticStart, min(semanticEnd, semanticStart+maxBytes)
	}
	return start, end
}

// matchedQueryTerms lists which query terms a capture matched. The index mask
// is authoritative when it reports one; otherwise the text is tokenised the
// way the index does (Unicode letters and digits, lowercased).
func matchedQueryTerms(text string, terms []string, mask uint32) []string {
	if len(terms) == 0 {
		return nil
	}
	var out []string
	if mask != 0 {
		for i, term := range terms {
			if i < 32 && mask&(1<<uint(i)) != 0 {
				out = append(out, term)
			}
		}
		return out
	}
	want := make(map[string]bool, len(terms))
	for _, term := range terms {
		want[term] = true
	}
	seen := map[string]bool{}
	tokenStart := -1
	flush := func(offset int) {
		if tokenStart < 0 {
			return
		}
		if token := strings.ToLower(text[tokenStart:offset]); want[token] {
			seen[token] = true
		}
		tokenStart = -1
	}
	for offset, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if tokenStart < 0 {
				tokenStart = offset
			}
		} else {
			flush(offset)
		}
		if len(seen) == len(want) {
			break
		}
	}
	flush(len(text))
	for _, term := range terms {
		if seen[term] {
			out = append(out, term)
		}
	}
	return out
}

// emphasisWords reports, for each space-separated word of a preview, whether
// it contains a query term, so a surface can emphasise the match. Terms match
// whole tokens, the same way the index does.
func emphasisWords(excerpt string, terms []string) (words []string, hit []bool) {
	if excerpt == "" {
		return nil, nil
	}
	want := make(map[string]bool, len(terms))
	for _, term := range terms {
		want[term] = true
	}
	words = strings.Fields(excerpt)
	hit = make([]bool, len(words))
	for i, word := range words {
		tokenStart := -1
		check := func(offset int) {
			if tokenStart >= 0 && want[strings.ToLower(word[tokenStart:offset])] {
				hit[i] = true
			}
			tokenStart = -1
		}
		for offset, r := range word {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				if tokenStart < 0 {
					tokenStart = offset
				}
			} else {
				check(offset)
			}
		}
		check(len(word))
	}
	return words, hit
}
