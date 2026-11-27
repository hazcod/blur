package search

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCaptureTokenStreamMatchesCanonicalTokensAcrossUnicodeAndPossessiveSeams(t *testing.T) {
	for _, tc := range []struct {
		text string
		want []string
	}{
		{"Dima's café\u0301 日本語 résumé", []string{"dima", "cafe", "日本語", "resume"}},
		{"it'sa Dima’s2 Dima’s end", []string{"it", "sa", "dima", "s2", "dima", "end"}},
		{"か\u3099 韓国語 A\u0301\u0308Z й", []string{"が", "韓国語", "az", "й"}},
		{"a" + strings.Repeat("\u0301", 40) + "b", []string{"ab"}},
		{"ßa x aß", []string{"ssa", "x", "ass"}},
		{"ß", []string{"ss"}},
		{strings.Repeat("ß", 300) + " " + strings.Repeat("a", 5), []string{strings.Repeat("ss", 300), "aaaaa"}},
		{"Søren Strauß Łukasz Æsir Dima’s", []string{"soren", "strauss", "lukasz", "aesir", "dima"}},
	} {
		for split := 0; split <= len(tc.text); split++ {
			if split < len(tc.text) && !utf8.RuneStart(tc.text[split]) {
				continue
			}
			var stream CaptureTokenStream
			var got []CaptureToken
			for i, piece := range []string{tc.text[:split], tc.text[split:]} {
				start := int64(0)
				if i == 1 {
					start = int64(split)
				}
				used, tokens, err := stream.Feed(piece, start, int64(i), i == 1, 64)
				if err != nil || used != len(piece) {
					t.Fatalf("split=%d feed=%d: used=%d err=%v", split, i, used, err)
				}
				got = append(got, tokens...)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("%q split=%d: tokens=%d want=%d", tc.text, split, len(got), len(tc.want))
			}
			for i, word := range tc.want {
				if got[i].Digest != CaptureTermDigest(word) {
					t.Fatalf("%q split=%d token%d does not match %q", tc.text, split, i, word)
				}
			}
		}
	}
}

func TestCaptureTokenStreamExpandedLetterKeepsSourceSpan(t *testing.T) {
	text := "x Strauß y"
	var stream CaptureTokenStream
	_, tokens, err := stream.Feed(text, 0, 0, true, 16)
	if err != nil || len(tokens) != 3 {
		t.Fatal(tokens, err)
	}
	if tokens[1].Digest != CaptureTermDigest("strauss") || tokens[1].Start != 2 || tokens[1].End != int64(len("x Strauß")) {
		t.Fatal("expanded letter shifted the token span", tokens[1])
	}
}
