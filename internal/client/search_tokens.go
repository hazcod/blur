package client

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/search"
)

// Every helper below reads text the way the exact index does: a word is a run
// of Unicode letters and digits and is reduced with search.NormalizeTokens, so
// folding ("résumé" is "resume") and the trailing possessive rule apply to
// previews, matched terms and closeness exactly as they apply to the index.

// foldWord returns the index token of one run of letters and digits, or "".
func foldWord(run string) string {
	ascii := true
	for i := 0; i < len(run); i++ {
		if run[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return strings.ToLower(run)
	}
	tokens, err := search.NormalizeTokens(run, 1)
	if err != nil || len(tokens) != 1 {
		return ""
	}
	return tokens[0]
}

type wordSpan struct {
	start, end int
	token      string
}

// termSpans lists every occurrence of a wanted term in text, in text order,
// with the index of the term it matched. wanted maps folded terms to indexes.
func termSpans(text string, wanted map[string]int) (spans []wordSpan, indexes []int) {
	tokenStart := -1
	flush := func(offset int) {
		if tokenStart < 0 {
			return
		}
		if token := foldWord(text[tokenStart:offset]); token != "" {
			if index, ok := wanted[token]; ok {
				spans = append(spans, wordSpan{tokenStart, offset, token})
				indexes = append(indexes, index)
			}
		}
		tokenStart = -1
	}
	for offset, r := range text {
		// A combining mark belongs to the word it follows: the index folds
		// before splitting, so decomposed "résumé" is one word "resume".
		if unicode.IsLetter(r) || unicode.IsDigit(r) || tokenStart >= 0 && search.IsCombiningAccent(r) {
			if tokenStart < 0 {
				tokenStart = offset
			}
		} else {
			flush(offset)
		}
	}
	flush(len(text))
	return spans, indexes
}

func wantedTerms(terms []string) map[string]int {
	wanted := make(map[string]int, len(terms))
	for _, term := range terms {
		if _, seen := wanted[term]; !seen {
			wanted[term] = len(wanted)
		}
	}
	return wanted
}

// termCloseness is the byte width of the tightest window of text that holds
// every query term the text contains. Zero means no term occurs (unknown).
func termCloseness(text string, terms []string) int {
	spans, indexes := termSpans(text, wantedTerms(terms))
	if len(spans) == 0 {
		return 0
	}
	present := map[int]bool{}
	for _, index := range indexes {
		present[index] = true
	}
	need, have, left := len(present), 0, 0
	counts := make(map[int]int, need)
	best := math.MaxInt
	for right := range spans {
		if counts[indexes[right]] == 0 {
			have++
		}
		counts[indexes[right]]++
		for have == need {
			best = min(best, spans[right].end-spans[left].start)
			counts[indexes[left]]--
			if counts[indexes[left]] == 0 {
				have--
			}
			left++
		}
	}
	return max(best, 1)
}

// matchedQueryTerms lists which query terms a capture matched, in query order.
// The index mask is authoritative when it reports one (it also covers the
// window title); otherwise the text and the title are read with the index
// tokenizer.
func matchedQueryTerms(text, title string, terms []string, mask uint32) []string {
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
	wanted := wantedTerms(terms)
	seen := map[int]bool{}
	for _, source := range []string{text, title} {
		if source == "" {
			continue
		}
		_, indexes := termSpans(source, wanted)
		for _, index := range indexes {
			seen[index] = true
		}
	}
	for _, term := range terms {
		if seen[wanted[term]] {
			out = append(out, term)
		}
	}
	return out
}

// matchPassageSpan locates the passage of at most maxBytes holding the most
// distinct query terms, like search.MatchPassage but with folded words (that
// helper lowercases only, and belongs to the index package). Zero means no
// term occurs in the text.
func matchPassageSpan(text string, terms []string, maxBytes int) (int, int) {
	if maxBytes < 1 || len(terms) == 0 || len(terms) > search.MaxTerms {
		return 0, 0
	}
	spans, indexes := termSpans(text, wantedTerms(terms))
	if len(spans) == 0 {
		return 0, 0
	}
	counts := make(map[int]int, len(terms))
	left, distinct, bestDistinct, bestCount := 0, 0, 0, 0
	bestStart, bestEnd := spans[0].start, spans[0].end
	for right, span := range spans {
		if counts[indexes[right]] == 0 {
			distinct++
		}
		counts[indexes[right]]++
		for left < right && span.end-spans[left].start > maxBytes {
			counts[indexes[left]]--
			if counts[indexes[left]] == 0 {
				distinct--
			}
			left++
		}
		if count := right - left + 1; distinct > bestDistinct || distinct == bestDistinct && count > bestCount {
			bestDistinct, bestCount = distinct, count
			bestStart, bestEnd = spans[left].start, span.end
		}
	}
	return bestStart, bestEnd
}

// emphasisWords reports, for each space-separated word of a preview, whether it
// holds a query term.
func emphasisWords(excerpt string, terms []string) (words []string, hit []bool) {
	if excerpt == "" {
		return nil, nil
	}
	wanted := wantedTerms(terms)
	words = strings.Fields(excerpt)
	hit = make([]bool, len(words))
	for i, word := range words {
		spans, _ := termSpans(word, wanted)
		hit[i] = len(spans) > 0
	}
	return words, hit
}
