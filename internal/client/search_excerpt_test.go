package client

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestExcerptShortTextIsReturnedWhole(t *testing.T) {
	got, start, end := ExcerptSpan("Dima said the retest is on Thursday.", 14, 20, 240)
	if got != "Dima said the retest is on Thursday." || got[start:end] != "retest" {
		t.Fatalf("excerpt = %q span=%q", got, got[start:end])
	}
	if Excerpt("", 0, 0, 240) != "" {
		t.Fatal("empty text must give an empty excerpt")
	}
}

func TestExcerptIsBoundedCentredAndMarksCutEdges(t *testing.T) {
	text := strings.Repeat("alpha beta gamma delta ", 40) + "NEEDLE" + strings.Repeat(" epsilon zeta eta theta", 40)
	at := strings.Index(text, "NEEDLE")
	got, start, end := ExcerptSpan(text, at, at+len("NEEDLE"), 240)
	if len(got) > 240 || !utf8.ValidString(got) {
		t.Fatalf("excerpt is %d bytes valid=%t: %q", len(got), utf8.ValidString(got), got)
	}
	if got[start:end] != "NEEDLE" {
		t.Fatalf("returned span %q does not cover the match in %q", got[start:end], got)
	}
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("cut edges need ellipses: %q", got)
	}
	left, right := start, len(got)-end
	if left < 60 || right < 60 || left > 150 || right > 150 {
		t.Fatalf("match is not roughly centred: left=%d right=%d in %q", left, right, got)
	}
}

func TestExcerptNearTextEdgesHasNoEllipsisThere(t *testing.T) {
	text := "NEEDLE " + strings.Repeat("filler words go here ", 50)
	got := Excerpt(text, 0, 6, 240)
	if strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") || len(got) > 240 {
		t.Fatalf("head excerpt = %q (%d bytes)", got, len(got))
	}
	text = strings.Repeat("filler words go here ", 50) + "NEEDLE"
	got = Excerpt(text, len(text)-6, len(text), 240)
	if !strings.HasPrefix(got, "…") || strings.HasSuffix(got, "…") || !strings.HasSuffix(got, "NEEDLE") || len(got) > 240 {
		t.Fatalf("tail excerpt = %q (%d bytes)", got, len(got))
	}
}

func TestExcerptNeverSplitsMultibyteRunes(t *testing.T) {
	text := strings.Repeat("日本語のテキスト、", 60) + "検索語" + strings.Repeat("、さらに続くテキスト", 60)
	at := strings.Index(text, "検索語")
	for _, limit := range []int{80, 97, 120, 240, 241} {
		got := Excerpt(text, at, at+len("検索語"), limit)
		if len(got) > limit || !utf8.ValidString(got) || !strings.Contains(got, "検索語") {
			t.Fatalf("limit %d: %d bytes valid=%t contains=%t: %q", limit, len(got), utf8.ValidString(got), strings.Contains(got, "検索語"), got)
		}
	}
}

func TestExcerptPrefersSentenceBoundaryNearTheCut(t *testing.T) {
	lead := strings.Repeat("x", 40)
	text := strings.Repeat("Filler sentence number one. ", 12) + "The retest is scheduled for Thursday 20 November. " + lead + strings.Repeat(" trailing words follow here.", 30)
	at := strings.Index(text, "retest")
	got := Excerpt(text, at, at+len("retest"), 240)
	if strings.HasPrefix(got, "…") {
		t.Fatalf("a sentence start is within reach but the excerpt opens mid-sentence: %q", got)
	}
	if !strings.HasPrefix(got, "Filler sentence number one.") || !strings.Contains(got, "The retest is scheduled") {
		t.Fatalf("excerpt should begin at a whole sentence and keep the match, got %q", got)
	}
	if len(got) > 240 {
		t.Fatalf("excerpt is %d bytes", len(got))
	}
}

func TestExcerptCollapsesWhitespaceAndClampsBadSpans(t *testing.T) {
	text := "Slack\n\n  Direct messages\n\tDima   Vermeulen  10:08\nMorning!"
	got := Excerpt(text, 0, 5, 240)
	if strings.ContainsAny(got, "\n\t") || strings.Contains(got, "  ") {
		t.Fatalf("whitespace was not collapsed: %q", got)
	}
	for _, span := range [][2]int{{-5, 3}, {10, 4}, {0, 9999}, {9999, 99999}} {
		got := Excerpt(text, span[0], span[1], 40)
		if len(got) > 40 || !utf8.ValidString(got) {
			t.Fatalf("span %v produced %q", span, got)
		}
	}
	if got := Excerpt(strings.Repeat("a", 500), 0, 500, 40); len(got) > 40 {
		t.Fatalf("a match wider than the budget still has to fit: %d bytes", len(got))
	}
	if got := Excerpt("tiny", 0, 4, 0); got != "" {
		t.Fatalf("zero budget = %q", got)
	}
}

func TestQueryExcerptCentresOnTheDensestTermCluster(t *testing.T) {
	text := "Slack Cresco Security Dima Vermeulen Samira Peeters\n" + strings.Repeat("Dima said hello. ", 20) +
		"Heads up: the Orfit external retest is scheduled for Thursday, Dima will send the scope tomorrow. " + strings.Repeat("Unrelated line. ", 30)
	got := QueryExcerpt(text, []string{"dima", "retest"}, 0, 0, 240)
	if !strings.Contains(got, "retest") || len(got) > 240 {
		t.Fatalf("excerpt should surface the retest passage, got %q", got)
	}
	if got := QueryExcerpt(strings.Repeat("no hit here ", 40), []string{"zebra"}, 0, 0, 100); !strings.HasPrefix(got, "no hit here") || len(got) > 100 {
		t.Fatalf("a text without a match starts at its head: %q", got)
	}
	// A semantic passage is honoured when no query term occurs in the text.
	body := strings.Repeat("filler ", 100) + "the passage that matters by meaning" + strings.Repeat(" filler", 100)
	at := strings.Index(body, "the passage")
	if got := QueryExcerpt(body, []string{"zebra"}, at, at+len("the passage that matters by meaning"), 240); !strings.Contains(got, "the passage that matters") {
		t.Fatalf("semantic span ignored: %q", got)
	}
}

func TestMatchedQueryTermsComeFromTheTextWhenTheIndexReportsNone(t *testing.T) {
	got := matchedQueryTerms("Heads up: the Orfit external retest is on Thursday, Dima will send it.", "", []string{"dima", "retest", "zebra"}, 0)
	if strings.Join(got, ",") != "dima,retest" {
		t.Fatalf("matched terms = %v", got)
	}
	got = matchedQueryTerms("anything", "", []string{"alpha", "beta", "gamma"}, 0b101)
	if strings.Join(got, ",") != "alpha,gamma" {
		t.Fatalf("mask-derived terms = %v", got)
	}
}

func TestTokenHelpersFoldLikeTheIndex(t *testing.T) {
	text := "Experience: résumé of Dima's work, CV_Youssef_RÉSUMÉ.pdf"
	if got := matchedQueryTerms(text, "", []string{"dima", "resume"}, 0); strings.Join(got, ",") != "dima,resume" {
		t.Fatalf("folded terms not matched: %v", got)
	}
	if got := matchedQueryTerms("plain body", "CV_Youssef_résumé.pdf", []string{"resume"}, 0); strings.Join(got, ",") != "resume" {
		t.Fatalf("a title-only term must count: %v", got)
	}
	words, hit := emphasisWords("see résumé here", []string{"resume"})
	if len(words) != 3 || hit[0] || !hit[1] || hit[2] {
		t.Fatalf("emphasis words = %v %v", words, hit)
	}
	if got := termCloseness("résumé then, much later, dima", []string{"resume", "dima"}); got != len("résumé then, much later, dima") {
		t.Fatalf("closeness must see the folded word: %d", got)
	}
	start, end := matchPassageSpan(strings.Repeat("filler ", 100)+"the résumé is here"+strings.Repeat(" filler", 100), []string{"resume"}, 120)
	if start != 700+4 || end != 700+4+len("résumé") {
		t.Fatalf("passage span = %d..%d", start, end)
	}
}
