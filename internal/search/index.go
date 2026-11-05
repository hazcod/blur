package search

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxIndexedObservationTerms keeps each observation's atomic index update
	// comfortably below the durable transaction's operation and byte ceilings.
	MaxIndexedObservationTerms = 128
)

var (
	ErrIndexUnavailable = errors.New("search: exact index unavailable")
	ErrIndexCorrupt     = errors.New("search: exact index inconsistent")
	ErrIndexBudget      = errors.New("search: exact index budget exhausted before ranking was proven")
)

// ExactQuery contains canonical exact terms and optional canonical app/domain
// filters. TopK asks the index for a globally proven ranked prefix (zero uses
// MaxResults). MaxRows, MaxBytes, and the fixed node budget bound work; if the
// index cannot prove the prefix within those budgets it returns ErrIndexBudget
// instead of exposing a potentially misranked partial page. Each hit's
// authenticated Bytes estimate lets callers budget source hydration.
type ExactQuery struct {
	Terms    []string
	AppID    string
	Domain   string
	TopK     int
	MaxRows  int
	MaxBytes int
}

// IndexHit identifies a source whose terms and filters all matched. Bytes is
// the encrypted index's authenticated estimate of its observation payload;
// callers use it to enforce a read budget before decrypting the source.
type IndexHit struct {
	SourceID string
	Score    int
	Bytes    int
}

// ExactPage is a bounded prefix of exact matches. HasMore means the row or
// byte budget stopped evaluation before the query's candidate set was done.
type ExactPage struct {
	Hits    []IndexHit
	Rows    int
	Nodes   int
	Bytes   int
	HasMore bool
}

// ExactIndex is the optional durable retrieval capability. Engines prefer it
// whenever the store implements it; scan search is a separate explicit route.
type ExactIndex interface {
	SearchExact(context.Context, ExactQuery) (ExactPage, error)
}

// NormalizeTokens applies Unicode lowercase mapping, splits on every rune
// except Unicode letters and digits, removes duplicates, and sorts by UTF-8
// byte order. It deliberately does not apply locale-specific stemming,
// transliteration, or language-dependent normalization.
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
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			token.WriteRune(r)
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
