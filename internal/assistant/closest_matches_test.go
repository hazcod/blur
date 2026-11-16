package assistant

import (
	"strings"
	"testing"
)

func TestQuotedAnswerNamesClosestMatchesOnlyWhenTermsWereRelaxed(t *testing.T) {
	full := Evidence{SourceID: strings.Repeat("a", 64), Text: "Pentest planning for the retest"}
	partial := Evidence{SourceID: strings.Repeat("b", 64), Text: "Dima: the retest is on Thursday", TermsRelaxed: true}
	plan := Plan{Kind: KindLookup}

	got, err := quotedEvidenceAnswer("what did Dima say about the pentest retest?", plan, []Evidence{partial, full}, Diagnosis{TermsRelaxed: true}, ContextTokenLimit)
	if err != nil || !strings.Contains(got.Answer.Text, "closest matches") {
		t.Fatalf("relaxed answer must say it shows the closest matches: %q err=%v", got.Answer.Text, err)
	}
	if got.Answer.SourceIDs[0] != partial.SourceID {
		t.Fatalf("top-ranked evidence must be first, got %v", got.Answer.SourceIDs)
	}

	got, err = quotedEvidenceAnswer("what did Dima say about the pentest retest?", plan, []Evidence{full}, Diagnosis{}, ContextTokenLimit)
	if err != nil || strings.Contains(got.Answer.Text, "closest matches") || !strings.HasPrefix(got.Answer.Text, "Saved passages:") {
		t.Fatalf("an all-terms answer must keep its plain heading: %q err=%v", got.Answer.Text, err)
	}
}
