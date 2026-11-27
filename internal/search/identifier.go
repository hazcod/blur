package search

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// FindIdentifier locates the first whole-label occurrence of subject in text
// and returns its byte span in the ORIGINAL text.
//
// The subject is split on its separators (underscore, hyphen, and horizontal
// whitespace) into components. Between two components the text may use no
// separator ("ThreatBot"), a single underscore, a single hyphen (-, U+2010,
// U+2011), or a run of horizontal whitespace, so "threat_bot" also finds
// "threatbot", "threat-bot", "Threat Bot". Separators never mix and never span
// a line break. Em and en dashes are prose punctuation, not joiners.
// Characters inside a component, and a subject whose separators are leading,
// trailing or doubled, compare literally.
//
// A match is a whole label: it may not touch a letter, digit, underscore or
// hyphen on either side, and a following ".", "/" or "\" followed by a word
// character is an extension or child path, so a longer identifier. A preceding
// path prefix is allowed ("pkg/review_long_search").
//
// The search runs in FoldLower(text), so the yes/no answer has exactly the
// tokenizer's fold semantics; only a match is mapped back to original offsets.
func FindIdentifier(text, subject string) (start, end int, found bool) {
	from, to, ok := findFolded(FoldLower(text), FoldLower(subject))
	if !ok {
		return 0, 0, false
	}
	start, end = originalSpan(text, from, to)
	return start, end, true
}

// ContainsIdentifier reports whether FindIdentifier finds subject in text,
// without building any offset mapping.
func ContainsIdentifier(text, subject string) bool {
	_, _, ok := findFolded(FoldLower(text), FoldLower(subject))
	return ok
}

// IdentifierJoined returns the subject's components concatenated without
// separators ("threat_bot" -> "threatbot"), the single token a source that
// writes "ThreatBot" indexes as. It is empty for a single-component subject.
func IdentifierJoined(subject string) string {
	parts := identifierComponents(FoldLower(subject))
	if len(parts) < 2 {
		return ""
	}
	return strings.Join(parts, "")
}

func findFolded(folded, subject string) (int, int, bool) {
	parts := identifierComponents(subject)
	if len(parts) == 0 {
		return 0, 0, false
	}
	for offset := 0; offset < len(folded); {
		index := strings.Index(folded[offset:], parts[0])
		if index < 0 {
			break
		}
		from := offset + index
		offset = from + 1
		if to, ok := matchComponents(folded, from, parts); ok && identifierBoundary(folded, from, to) {
			return from, to, true
		}
	}
	return 0, 0, false
}

func isIdentifierDash(r rune) bool { return r == '-' || r == 0x2010 || r == 0x2011 }

func isHorizontalSpace(r rune) bool { return r == '\t' || unicode.Is(unicode.Zs, r) }

func isIdentifierSeparator(r rune) bool {
	return r == '_' || isIdentifierDash(r) || isHorizontalSpace(r)
}

// identifierComponents splits a folded subject on separators. A subject whose
// separators lead, trail or double up has no well-defined components, so it
// stays one literal component and only matches its exact spelling.
func identifierComponents(subject string) []string {
	if subject == "" {
		return nil
	}
	parts := strings.FieldsFunc(subject, isIdentifierSeparator)
	separators := 0
	for _, r := range subject {
		if isIdentifierSeparator(r) {
			separators++
		}
	}
	first, _ := utf8.DecodeRuneInString(subject)
	last, _ := utf8.DecodeLastRuneInString(subject)
	if len(parts) == 0 || separators != len(parts)-1 || isIdentifierSeparator(first) || isIdentifierSeparator(last) {
		return []string{subject}
	}
	return parts
}

// matchComponents matches parts from folded[from:] joined by exactly one
// separator kind and returns the end offset.
func matchComponents(folded string, from int, parts []string) (int, bool) {
	at := from
	for i, part := range parts {
		if i > 0 && !strings.HasPrefix(folded[at:], part) {
			r, size := utf8.DecodeRuneInString(folded[at:])
			switch {
			case r == '_' || isIdentifierDash(r):
				at += size
			case isHorizontalSpace(r):
				for at < len(folded) {
					next, nextSize := utf8.DecodeRuneInString(folded[at:])
					if !isHorizontalSpace(next) {
						break
					}
					at += nextSize
				}
			default:
				return 0, false
			}
		}
		if !strings.HasPrefix(folded[at:], part) {
			return 0, false
		}
		at += len(part)
	}
	return at, true
}

func identifierBoundary(text string, start, end int) bool {
	word := func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || isIdentifierDash(r) || IsCombiningAccent(r)
	}
	if start > 0 {
		if r, _ := utf8.DecodeLastRuneInString(text[:start]); word(r) {
			return false
		}
	}
	if end < len(text) {
		r, size := utf8.DecodeRuneInString(text[end:])
		if word(r) {
			return false
		}
		// A full stop can end a sentence; a joined extension or child path is
		// part of a longer identifier.
		if strings.ContainsRune("./\\", r) && end+size < len(text) {
			if next, _ := utf8.DecodeRuneInString(text[end+size:]); word(next) {
				return false
			}
		}
	}
	return true
}

// originalSpan maps a folded byte span back to the original text. It walks
// normalization segments, whose folds concatenate to the whole-text fold, and
// snaps to segment edges so the result is on rune boundaries. A segment's fold
// may be longer or shorter than the segment itself.
func originalSpan(text string, from, to int) (int, int) {
	ascii := true
	for i := 0; i < len(text); i++ {
		if text[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return from, to
	}
	start, folded := -1, 0
	for at := 0; at < len(text); {
		size := norm.NFC.NextBoundaryInString(text[at:], true)
		if size <= 0 {
			size = len(text) - at
		}
		next := folded + len(FoldLower(text[at:at+size]))
		if start < 0 && next > from {
			start = at
		}
		if start >= 0 && next >= to {
			return start, at + size
		}
		folded = next
		at += size
	}
	return max(start, 0), len(text)
}
