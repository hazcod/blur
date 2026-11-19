package client

import (
	"strings"
	"testing"
)

// macOS file names and some pasted text are decomposed (NFD): "résumé" is the
// letters plus combining marks. The index folds before splitting, so the
// preview helpers must keep a combining mark inside its word too.
func TestTermSpansTreatCombiningMarksAsPartOfTheWord(t *testing.T) {
	nfd := "Updated résumé draft"
	spans, indexes := termSpans(nfd, wantedTerms([]string{"resume"}))
	if len(spans) != 1 || len(indexes) != 1 || nfd[spans[0].start:spans[0].end] != "résumé" {
		t.Fatalf("decomposed word not matched as one token: %+v", spans)
	}
	composed := "Updated résumé draft"
	if spans, _ := termSpans(composed, wantedTerms([]string{"resume"})); len(spans) != 1 || composed[spans[0].start:spans[0].end] != "résumé" {
		t.Fatalf("composed word regressed: %+v", spans)
	}
	// A mark with no base letter before it never starts a word.
	if spans, _ := termSpans("́ resume", wantedTerms([]string{"resume"})); len(spans) != 1 {
		t.Fatalf("stray combining mark broke the next word: %+v", spans)
	}
}

func TestNFDTextCentresAndEmphasisesLikeComposedText(t *testing.T) {
	filler := strings.Repeat("unrelated words go here. ", 60)
	nfd := filler + "Final José résumé review " + filler
	start, end := matchPassageSpan(nfd, []string{"jose", "resume"}, 64)
	if end <= start || !strings.Contains(nfd[start:end], "José résumé") {
		t.Fatalf("excerpt not centred on decomposed match: %d %d", start, end)
	}
	if closeness := termCloseness(nfd, []string{"jose", "resume"}); closeness == 0 || closeness > len("José résumé") {
		t.Fatalf("closeness missed decomposed terms: %d", closeness)
	}
	words, hit := emphasisWords("Updated résumé draft", []string{"resume"})
	if len(words) != 3 || hit[0] || !hit[1] || hit[2] {
		t.Fatalf("emphasis missed decomposed word: %q %v", words, hit)
	}
	if matched := matchedQueryTerms("José notes", "", []string{"jose", "other"}, 0); len(matched) != 1 || matched[0] != "jose" {
		t.Fatalf("matched terms missed decomposed word: %v", matched)
	}
}
