package search

import (
	"strings"
	"testing"
)

func TestNormalizeTokensIsUnicodeAwareAndDeterministic(t *testing.T) {
	got, err := NormalizeTokens("ÉCOLE café Café a-b naïve 中文 １２３", 8)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "cafe", "ecole", "naive", "中文", "１２３"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("NormalizeTokens() = %q, want %q", got, want)
	}
	if _, err := NormalizeTokens("one two three", 2); err == nil {
		t.Fatal("normalizer exceeded its unique-term bound")
	}
}

func TestNormalizeTokensFoldsDiacritics(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"résumé", "resume"},
		{"RÉSUMÉ", "resume"},
		{"Zoë Müller", "muller,zoe"},
		{"Ångström", "angstrom"},
		{"São Paulo", "paulo,sao"},
		{"Dvořák", "dvorak"},
		{"Łódź", "lodz"},
		{"İstanbul", "istanbul"},
		{"re\u0301sume\u0301", "resume"}, // already decomposed input
		{"straße", "strasse"},
		{"naïve café", "cafe,naive"},
		{"CV_Youssef_résumé.pdf", "cv,pdf,resume,youssef"},
		{"中文 日本語", "中文,日本語"},
	} {
		got, err := NormalizeTokens(tc.in, 16)
		if err != nil {
			t.Fatal(tc.in, err)
		}
		if strings.Join(got, ",") != tc.want {
			t.Errorf("NormalizeTokens(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeTokensDropsTrailingPossessive(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Dima's", "dima"},
		{"Dima’s", "dima"},
		{"DIMA'S retest", "dima,retest"},
		{"Dima's, Chloe’s; Samira's.", "chloe,dima,samira"},
		{"it's", "it"},
		{"James's notes", "james,notes"},
		{"teams' notes", "notes,teams"},
		{"rock'n'roll", "n,rock,roll"},
		{"'s alone", "alone,s"},
		{"'s", "s"},
		{"Dima's2", "dima,s2"}, // 's' followed by a digit is not a possessive
		{"it'sa", "it,sa"},
		{"Dima’s’s", "dima,s"},
	} {
		got, err := NormalizeTokens(tc.in, 16)
		if err != nil {
			t.Fatal(tc.in, err)
		}
		if strings.Join(got, ",") != tc.want {
			t.Errorf("NormalizeTokens(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeTokensIsIdempotent(t *testing.T) {
	for _, in := range []string{
		"résumé Dima's café ÉCOLE", "Zoë", "İstanbul", "straße", "Ångström’s", "e\u0301", "a\u0301\u0302b",
		"Dvořák's 中文 １２３", "o'clock", "",
	} {
		first, err := NormalizeTokens(in, 32)
		if err != nil {
			t.Fatal(in, err)
		}
		second, err := NormalizeTokens(strings.Join(first, " "), 32)
		if err != nil {
			t.Fatal(in, err)
		}
		if strings.Join(first, ",") != strings.Join(second, ",") {
			t.Errorf("not idempotent for %q: %q then %q", in, first, second)
		}
		for _, term := range first {
			single, err := NormalizeTokens(term, 1)
			if err != nil || len(single) != 1 || single[0] != term {
				t.Errorf("token %q of %q does not re-normalize to itself: %q %v", term, in, single, err)
			}
		}
	}
}

func TestNormalizeTokensFoldsLettersWithoutDecomposition(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Søren", "soren"}, {"SØREN", "soren"}, {"Łukasz", "lukasz"}, {"Strauß", "strauss"},
		{"STRASSE", "strasse"}, {"Straße", "strasse"}, {"Æsir", "aesir"}, {"Œuvre", "oeuvre"},
		{"Đorđe", "dorde"}, {"Þór Ðegn", "degn,thor"}, {"Mħarr", "mharr"}, {"Ismaıl", "ismail"},
		{"Ŧ", "t"}, {"Ŋ", "n"}, {"Ĳssel", "ijssel"}, {"Kolŀlegi", "kolllegi"}, {"ſea", "sea"},
		{"ẞ", "ss"},
		{"Müller", "muller"},                                           // no umlaut transliteration
		{"й", "й"}, {"か\u3099", "が"}, {"한국어", "한국어"}, {"क\u093f", "क"}, // vowel sign is not a letter: unchanged by the fold
		{"Dima's Søren's", "dima,soren"}, {"cafe", "cafe"},
	} {
		got, err := NormalizeTokens(tc.in, 16)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, ",") != tc.want {
			t.Errorf("NormalizeTokens(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if len(got) == 1 && !strings.Contains(tc.in, "'") && !strings.Contains(tc.in, "\u093f") && FoldWord(tc.in) != got[0] {
			t.Errorf("FoldWord(%q) = %q, want %q", tc.in, FoldWord(tc.in), got[0])
		}
	}
}
