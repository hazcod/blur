package client

import (
	"strings"
	"unicode"

	"github.com/hazcod/blur/internal/search"
)

// numberedSubjectSupport counts query topic/number pairs found next to one
// another in an individual source field, in either order. Every intervening
// word counts: unrelated date numbers cannot jump over words to join a label.
// Punctuation and whitespace are permitted between adjacent words, as in
// "invoice #23" or two Accessibility table cells. This is lexical support,
// not semantic ID extraction; an unsupported paraphrase stays a closest match.
func numberedSubjectSupport(subjects [][2]string, texts ...string) int {
	if len(subjects) == 0 {
		return 0
	}
	if len(subjects) > search.MaxTerms {
		subjects = subjects[:search.MaxTerms]
	}
	wanted := make(map[[2]string]int, len(subjects)*2)
	for i, pair := range subjects {
		wanted[pair] = i
		wanted[[2]string{pair[1], pair[0]}] = i
	}
	seen := make([]bool, len(subjects))
	for _, text := range texts {
		previous, start := "", -1
		flush := func(end int) {
			if start < 0 {
				return
			}
			token := search.FoldWord(strings.TrimRight(text[start:end], "'’"))
			if strings.ContainsAny(token, "'’") {
				previous = "" // contractions are not a single index word
			} else {
				if i, ok := wanted[[2]string{previous, token}]; ok {
					seen[i] = true
				}
				previous = token
			}
			start = -1
		}
		for i, r := range text {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || start >= 0 && (search.IsCombiningAccent(r) || r == '\'' || r == '’') {
				if start < 0 {
					start = i
				}
			} else {
				flush(i)
			}
		}
		flush(len(text))
	}
	count := 0
	for _, found := range seen {
		if found {
			count++
		}
	}
	return count
}
