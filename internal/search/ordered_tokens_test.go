package search

import (
	"errors"
	"slices"
	"testing"
)

func TestOrderedTokensKeepsSourceOrderAndAgreesWithNormalizeTokens(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"Threat-Bot", []string{"threat", "bot"}},
		{"José Müller", []string{"jose", "muller"}},
		{"Müller José", []string{"muller", "jose"}},
		{"Dima Petrov's", []string{"dima", "petrov"}},
		{"it'sa Dima's2", []string{"it", "sa", "dima", "s2"}},
		{"a a b", []string{"a", "a", "b"}},
		{"  --  ", nil},
	} {
		got, err := OrderedTokens(tc.in, 16)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Fatalf("%q: %v %v want %v", tc.in, got, err, tc.want)
		}
		set, err := NormalizeTokens(tc.in, 16)
		if err != nil {
			t.Fatal(err)
		}
		sorted := slices.Compact(slices.Sorted(slices.Values(got)))
		if !slices.Equal(sorted, set) && (len(sorted) != 0 || len(set) != 0) {
			t.Fatalf("%q: ordered %v disagrees with set %v", tc.in, sorted, set)
		}
	}
}

func TestOrderedTokensRejectsInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		in    string
		limit int
	}{{"a b c", 2}, {"a\x00b", 4}, {"\xff", 4}, {"a", 0}} {
		if _, err := OrderedTokens(tc.in, tc.limit); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("%q/%d: %v", tc.in, tc.limit, err)
		}
	}
}
