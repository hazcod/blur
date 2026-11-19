package search

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	// InlineObservationTerms is the inline-index optimization threshold.
	// Larger observations are fully indexed in resumable batches; this is
	// never a document acceptance or total coverage limit.
	InlineObservationTerms = 128
	MaxScopeSourceIDs      = 65536
	MaxExcludedApps        = 32
)

var (
	ErrIndexUnavailable = errors.New("search: exact index unavailable")
	ErrIndexCorrupt     = errors.New("search: exact index inconsistent")
	ErrIndexBudget      = errors.New("search: exact index budget exhausted before ranking was proven")
)

// ExactQuery contains canonical exact terms and optional canonical app/domain
// filters. MinTermMatches zero requires every term; a positive value permits
// a bounded ranked partial-term query. TopK asks the index for a globally
// proven ranked prefix (zero uses MaxResults). MaxRows, MaxBytes, and the fixed
// node budget bound work; if the index cannot prove the prefix within those
// budgets it returns ErrIndexBudget instead of exposing a potentially
// misranked partial page. Each hit's authenticated Bytes estimate lets callers
// budget source hydration.
type ExactQuery struct {
	Terms          []string
	AppID          string
	Domain         string
	TopK           int
	MaxRows        int
	MaxBytes       int
	MinTermMatches int
	// PartialOnly excludes sources containing every requested term while
	// preserving ranked matching for MinTermMatches. Ask uses it to retrieve
	// candidates beyond a crowded strict-match prefix.
	PartialOnly   bool
	CapturedStart time.Time
	CapturedEnd   time.Time
	// ScopeSourceIDs additionally admit sources with a later activity sighting
	// inside CapturedStart/End, even if the content was first saved earlier.
	ScopeSourceIDs []string
	ExcludeAppIDs  []string
}

// IndexHit identifies a source meeting the query's term threshold and filters.
// Bytes is the encrypted index's authenticated estimate of its observation
// payload; callers use it to enforce a read budget before decrypting the source.
type IndexHit struct {
	SourceID   string
	Score      int
	Bytes      int
	CapturedAt time.Time
	MatchStart int
	MatchEnd   int
	// MatchedTerms has bit i set when ExactQuery.Terms[i] is indexed for the
	// source. Zero means the index did not report it (for example hits from
	// semantic or recency retrieval); callers must not read it as "no match".
	MatchedTerms uint32
}

// ExactPage is a bounded prefix of exact matches. HasMore means the row or
// byte budget stopped evaluation before the query's candidate set was done.
type ExactPage struct {
	Hits []IndexHit
	// AlternateQueries contains short phrases generated while relevance-ranking
	// Ask candidates; callers may use them for one bounded recovery search.
	AlternateQueries []string
	Rows             int
	Nodes            int
	Bytes            int
	HasMore          bool
	// IndexingPending means saved sources still have exact-index work queued.
	// Matches remain useful, but absence and global ranking are provisional.
	IndexingPending bool
	// CoverageLimited means optional meaning retrieval could not run. Exact
	// matches remain usable, but an empty page does not prove absence.
	CoverageLimited bool
}

// ExactIndex is the optional durable retrieval capability. Engines prefer it
// whenever the store implements it; scan search is a separate explicit route.
type ExactIndex interface {
	SearchExact(context.Context, ExactQuery) (ExactPage, error)
}

// NormalizeTokens applies Unicode lowercase mapping, folds Latin and Greek
// diacritics ("résumé" becomes "resume"), drops a trailing possessive 's or
// ’s ("Dima's" becomes "dima"), splits on every rune except Unicode letters
// and digits, removes duplicates, and sorts by UTF-8 byte order. It
// deliberately does not apply locale-specific stemming or transliteration.
//
// This is the one tokenizer for both the exact index and queries, so a change
// here changes what an index must contain: bump the durable index format
// version with it (internal/storage/durable).
func NormalizeTokens(value string, limit int) ([]string, error) {
	if limit <= 0 || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return nil, ErrInvalidRequest
	}
	seen := make(map[string]struct{})
	var token strings.Builder
	flush := func() error {
		if token.Len() == 0 {
			return nil
		}
		seen[token.String()] = struct{}{}
		token.Reset()
		if len(seen) > limit {
			return ErrInvalidRequest
		}
		return nil
	}
	runes := []rune(FoldLower(value))
	isWord := isTokenRune
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if isWord(r) {
			token.WriteRune(r)
			continue
		}
		// A possessive is an apostrophe and "s" that end a word: ' or ’
		// right after letters/digits, then s, then anything but a letter or
		// digit. "Dima's" -> "dima"; "it'sa", "'s" and "Dima's2" are kept.
		if token.Len() > 0 && isPossessiveAt(runes, i) {
			i++
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		if err := flush(); err != nil {
			return nil, err
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	terms := make([]string, 0, len(seen))
	for term := range seen {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	return terms, nil
}

func isTokenRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// FoldLower is the shared case and diacritic fold of every token.
func FoldLower(value string) string { return foldDiacritics(strings.ToLower(value)) }

// isPossessiveAt reports whether runes[i] starts a possessive: an apostrophe
// (' or ’) and "s" that end a word. "Dima's" -> "dima"; "it'sa", "'s" and
// "Dima's2" are not possessives. The caller guarantees a word precedes i.
func isPossessiveAt(runes []rune, i int) bool {
	return (runes[i] == '\'' || runes[i] == '’') && i+1 < len(runes) && runes[i+1] == 's' &&
		(i+2 == len(runes) || !isTokenRune(runes[i+2]))
}

// FoldWord returns the NormalizeTokens form of a single word: lowercase,
// diacritics folded, trailing possessive removed. Callers that match words
// against NormalizeTokens output (the assistant planner) must compare in this
// space. NormalizeTokens shares foldLower and isPossessiveAt with it, so the
// rule has one definition.
func FoldWord(word string) string {
	runes := []rune(FoldLower(word))
	for i := 1; i < len(runes); i++ {
		if isTokenRune(runes[i-1]) && isPossessiveAt(runes, i) {
			return string(runes[:i])
		}
	}
	return string(runes)
}

// IsCombiningAccent reports whether r is a combining diacritical mark (U+0300
// to U+036F), the range foldDiacritics removes or recomposes. Text scanners
// that locate index words in the source keep such a mark inside the word it
// follows, because the index folds (and recomposes) before it splits words, so
// decomposed "résumé" is the single word "resume".
func IsCombiningAccent(r rune) bool { return r >= 0x0300 && r <= 0x036f }

// foldDiacritics removes combining accents that follow a Latin or Greek base
// letter so accented and plain spellings index alike. Other scripts keep their
// marks (Cyrillic "й", Japanese dakuten, Hangul and Indic vowel signs change
// meaning), and canonical composition is restored afterwards so those texts
// tokenize exactly as before. The input is already lowercase.
func foldDiacritics(value string) string {
	ascii := true
	for i := 0; i < len(value); i++ {
		if value[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return value
	}
	var out strings.Builder
	out.Grow(len(value))
	var base rune
	for _, r := range norm.NFD.String(value) {
		if IsCombiningAccent(r) && (unicode.Is(unicode.Latin, base) || unicode.Is(unicode.Greek, base)) {
			continue
		}
		base = r
		out.WriteRune(r)
	}
	return norm.NFC.String(out.String())
}
