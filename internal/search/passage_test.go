package search

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMatchPassageFindsTailAndPrefersDistinctTerms(t *testing.T) {
	prefix := strings.Repeat("unrelated text. ", 10000)
	text := prefix + "The release is blocked by a TLS handshake timeout. Fix certificate expiry first."
	start, end := MatchPassage(text, []string{"tls", "timeout"}, 128)
	if start <= 0 || end <= start || end-start > 128 || !strings.Contains(text[start:end], "TLS handshake timeout") {
		t.Fatal(start, end)
	}
	text = "alpha alpha alpha alpha " + strings.Repeat("unrelated ", 100) + "alpha beta decision"
	start, end = MatchPassage(text, []string{"alpha", "beta"}, 64)
	if start <= 0 || !strings.Contains(text[start:end], "alpha beta") {
		t.Fatal(start, end)
	}
}

func TestMatchPassagePreservesUnicodeBoundariesAndNoMatch(t *testing.T) {
	text := strings.Repeat("日本語🙂 ", 200) + "日本語 ÉXPiRY résumé"
	for limit := 1; limit < 100; limit++ {
		start, end := MatchPassage(text, []string{"expiry", "résumé", "résumé"}, limit)
		if start < 0 || end < start || end > len(text) || end-start > limit || !utf8.ValidString(text[start:end]) {
			t.Fatal(limit, start, end)
		}
	}
	if start, end := MatchPassage(text, []string{"missing"}, 64); start != 0 || end != 0 {
		t.Fatal(start, end)
	}
	if start, end := MatchPassage("short match", []string{"match"}, 64); start != 0 || end != len("short match") {
		t.Fatal(start, end)
	}
}

func TestMatchPassageFoldsBothSides(t *testing.T) {
	text := "Notes from José about the résumé"
	for _, terms := range [][]string{{"jose", "resume"}, {"josé", "résumé"}, {"JOSE", "Résumé"}} {
		if start, end := MatchPassage(text, terms, 1024); start != 0 || end != len(text) {
			t.Fatal(terms, start, end)
		}
	}
	long := strings.Repeat("unrelated filler. ", 400) + "Then José shared the résumé with us. " + strings.Repeat("more filler. ", 400)
	start, end := MatchPassage(long, []string{"jose", "resume"}, 128)
	if start <= 0 || end <= start || end-start > 128 || !strings.Contains(long[start:end], "José shared the résumé") {
		t.Fatal(start, end)
	}
	if start, end := MatchPassage(long, []string{"missing"}, 128); start != 0 || end != 0 {
		t.Fatal(start, end)
	}
}

func TestMatchPassageKeepsCombiningMarksInsideWords(t *testing.T) {
	long := strings.Repeat("unrelated filler. ", 400) + "Then José shared the résumé with us. " + strings.Repeat("more filler. ", 400)
	start, end := MatchPassage(long, []string{"jose", "resume"}, 128)
	if start <= 0 || end <= start || end-start > 128 || !strings.Contains(long[start:end], "José shared the résumé") {
		t.Fatal(start, end)
	}
}

func FuzzMatchPassageOffsets(f *testing.F) {
	f.Add("beginning 日本語 résumé tail", "résumé", 64)
	f.Add("a a a alpha beta", "alpha", 8)
	f.Fuzz(func(t *testing.T, text, term string, maxBytes int) {
		if len(text) > 1<<20 || len(term) > 512 || maxBytes < 1 || maxBytes > 4096 {
			return
		}
		start, end := MatchPassage(text, []string{term}, maxBytes)
		if start < 0 || end < start || end > len(text) || end-start > maxBytes {
			t.Fatal(start, end)
		}
		if end > start && !utf8.ValidString(text[start:end]) {
			t.Fatal("split UTF8", start, end)
		}
	})
}
