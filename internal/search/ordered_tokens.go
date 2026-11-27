package search

import (
	"strings"
	"unicode/utf8"
)

// OrderedTokens splits value exactly like NormalizeTokens (same fold, same
// possessive rule, same word runes) but keeps every token in source order and
// does not deduplicate. Callers that need order-sensitive keys, such as a
// squashed "threatbot" spelling, use it; set semantics stay in
// NormalizeTokens. More than limit tokens is ErrInvalidRequest.
func OrderedTokens(value string, limit int) ([]string, error) {
	if limit <= 0 || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return nil, ErrInvalidRequest
	}
	var tokens []string
	var token strings.Builder
	flush := func() error {
		if token.Len() == 0 {
			return nil
		}
		tokens = append(tokens, token.String())
		token.Reset()
		if len(tokens) > limit {
			return ErrInvalidRequest
		}
		return nil
	}
	runes := []rune(FoldLower(value))
	for i := 0; i < len(runes); i++ {
		if isTokenRune(runes[i]) {
			token.WriteRune(runes[i])
			continue
		}
		if token.Len() > 0 && isPossessiveAt(runes, i) {
			i++
		}
		if err := flush(); err != nil {
			return nil, err
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return tokens, nil
}
