package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
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

// "ø" and "ß" have no canonical decomposition; the index spells them plainly,
// so a plain query finds the capture and the preview highlights the original
// spelling at its original offsets.
func TestSearchFindsLettersWithoutDecompositionThroughEncryptedStore(t *testing.T) {
	ctx := context.Background()
	store := foldAskStore(t)
	text := "Søren Krag reviewed Strauß contract"
	observation, err := capture.SanitizeObservation("com.apple.Notes", "", text, capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, observation, time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	scenarioIndex(t, store)
	c := NewClient(store)
	defer c.Close()
	for _, query := range []string{"soren strauss", "Søren Strauß", "SOREN STRAUSS"} {
		hits, err := c.SearchMemoryContext(ctx, query)
		if err != nil || len(hits) != 1 || hits[0].SourceID != observation.ContentID() {
			t.Fatalf("query %d bytes found %d hits: %v", len(query), len(hits), err)
		}
		hit := hits[0]
		if got := strings.Join(hit.MatchedTerms, ","); !strings.Contains(got, "soren") || !strings.Contains(got, "strauss") {
			t.Fatalf("matched terms %q", got)
		}
		if hit.ExcerptEnd <= hit.ExcerptStart || hit.ExcerptEnd > len(hit.Excerpt) || !strings.HasPrefix(hit.Excerpt[hit.ExcerptStart:], "Søren") {
			t.Fatalf("preview emphasis not on the original spelling: %q %d-%d", hit.Excerpt, hit.ExcerptStart, hit.ExcerptEnd)
		}
	}
}
