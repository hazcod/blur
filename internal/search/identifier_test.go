package search

import (
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

func TestFindIdentifierSeparatorInsensitive(t *testing.T) {
	cases := []struct {
		subject, text string
		want          string // matched original text, "" for no match
	}{
		{"threat_bot", "threat_bot is up", "threat_bot"},
		{"threat_bot", "threat-bot is up", "threat-bot"},
		{"threat_bot", "threat bot is up", "threat bot"},
		{"threat_bot", "x Threat Bot x", "Threat Bot"},
		{"threat_bot", "THREAT_BOT", "THREAT_BOT"},
		{"threat_bot", "threat \t bot", "threat \t bot"},
		{"threat_bot", "threat bot", "threat bot"},
		{"threat-bot", "threat_bot", "threat_bot"},
		{"threat_bot", "threat. Bot", ""},
		{"threat_bot", "threat_ bot", ""},
		{"threat_bot", "threat -bot", ""},
		{"threat_bot", "threat--bot", ""},
		{"threat_bot", "threat\nbot", ""},
		{"threat_bot", "threat \n bot", ""},
		{"threat_bot", "threat.bot", ""},
		{"threat_bot", "threat/bot", ""},
		{"threat_bot", "threat\\bot", ""},
		{"threat_bot", "threat_bots", ""},
		{"review_long_search", "review_long_search-v2", ""},
		{"review_long_search", "review_long_search.py", ""},
		{"review_long_search", "review_long_search/child", ""},
		{"review_long_search", "review-long-search-v2", ""},
		{"review_long_search", "old-review_long_search", ""},
		{"review_long_search", "my_review_long_search", ""},
		{"review_long_search", "pkg/review_long_search", "review_long_search"},
		{"review_long_search", "the review_long_search.", "review_long_search"},
		{"review_long_search", "review long search ran", "review long search"},
		{"agent_build_01", "agent_build-01", "agent_build-01"},
		{"agent_build-01", "agent build 01", "agent build 01"},
		{"agent_build_01", "agent_ MIDDLE build_01", ""},
		{"threat_bot", "threat_bots threat-bot", "threat-bot"},
		{"a.b_c", "a.b c", "a.b c"},
	}
	for _, tc := range cases {
		start, end, found := FindIdentifier(tc.text, tc.subject)
		if tc.want == "" {
			if found {
				t.Errorf("%q matched %q at %q", tc.subject, tc.text, tc.text[start:end])
			}
			continue
		}
		if !found || tc.text[start:end] != tc.want {
			t.Errorf("%q in %q = %v %q, want %q", tc.subject, tc.text, found, tc.text[start:end], tc.want)
		}
	}
}

func TestFindIdentifierSpanSurvivesFoldingAndAccents(t *testing.T) {
	for _, tc := range []struct{ subject, text, want string }{
		{"café_bot", "le Café-Bot tourne", "Café-Bot"},
		{"cafe_bot", "le café bot tourne", "café bot"},
		{"cafe_bot", "le café_bot tourne", "café_bot"},
		{"cafe_bot", "ÉÉÉ café_bot", "café_bot"},
		{"i_x", "İ_x ok", "İ_x"},
	} {
		start, end, found := FindIdentifier(tc.text, tc.subject)
		if !found || tc.text[start:end] != tc.want {
			t.Errorf("%q in %q = %v %q, want %q", tc.subject, tc.text, found, tc.text[start:end], tc.want)
		}
	}
	if ContainsIdentifier("anything", "") {
		t.Error("empty subject matched")
	}
}

func TestFoldedSegmentsConcatenateToWholeTextFold(t *testing.T) {
	for name, text := range map[string]string{
		"latin":     "cafe\u0301 re\u0301sume\u0301 u\u0308ber",
		"hangul":    "\u1112\u1161\u11ab\u1100\u1173\u11af x",
		"kana":      "\u3053\u3099 \u30ab\u3099",
		"cyrillic":  "\u0438\u0306 \u0418\u0306",
		"mixed":     "ASCII \u00c9cole e\u0301 \u1112\u1161\u11ab \u3053\u3099 end",
		"ascii":     "Plain ASCII text",
		"expanding": "Stra\u00dfe \u0130stanbul",
	} {
		var joined strings.Builder
		for at := 0; at < len(text); {
			size := norm.NFC.NextBoundaryInString(text[at:], true)
			if size <= 0 {
				size = len(text) - at
			}
			joined.WriteString(FoldLower(text[at : at+size]))
			at += size
		}
		if joined.String() != FoldLower(text) {
			t.Errorf("%s: per-segment fold %q != whole fold %q", name, joined.String(), FoldLower(text))
		}
	}
}

func TestFindIdentifierComposedAndDecomposedAgree(t *testing.T) {
	composed, decomposed := "\ud55c_bot", "\u1112\u1161\u11ab_bot"
	for _, tc := range []struct{ subject, text string }{
		{composed, "x " + decomposed + " y"},
		{decomposed, "x " + composed + " y"},
		{"\u3053\u3099_bot", "\u3054 bot"},
		{"\u3054_bot", "\u3053\u3099-bot"},
		{"\u0439_bot", "\u0438\u0306 bot"},
		{"cafe_bot", "\u00c9 cafe\u0301 bot ok"},
		{"stra\u00dfe_bot", "Stra\u00dfe-Bot"},
	} {
		start, end, found := FindIdentifier(tc.text, tc.subject)
		if !found {
			t.Errorf("%q not found in %q", tc.subject, tc.text)
			continue
		}
		if !utf8.ValidString(tc.text[start:end]) {
			t.Errorf("span %q is not on rune boundaries", tc.text[start:end])
		}
		if got, want := foldedSeparatorsNormalized(tc.text[start:end]), foldedSeparatorsNormalized(tc.subject); got != want {
			t.Errorf("span %q folds to %q, want %q", tc.text[start:end], got, want)
		}
	}
}

// foldedSeparatorsNormalized folds and maps every separator to "_" so a span
// can be compared with the subject regardless of which separator it used.
func foldedSeparatorsNormalized(value string) string {
	return strings.Map(func(r rune) rune {
		if isIdentifierSeparator(r) {
			return '_'
		}
		return r
	}, FoldLower(value))
}
