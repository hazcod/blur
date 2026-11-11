package derive

import (
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/knowledge"
)

func sourceID(b byte) string { return strings.Repeat(string(b), 64) }

func TestValidateInputBoundsAndDeduplicatesSources(t *testing.T) {
	input := []Evidence{{SourceID: sourceID('a'), Text: "Project Atlas ships in June."}, {SourceID: sourceID('b'), Text: "Mina owns the release checklist."}}
	if err := ValidateInput(input); err != nil {
		t.Fatalf("valid evidence rejected: %v", err)
	}
	for name, invalid := range map[string][]Evidence{
		"duplicate source": {{SourceID: sourceID('a'), Text: "one"}, {SourceID: sourceID('a'), Text: "two"}},
		"bad source ID":    {{SourceID: "source", Text: "one"}},
		"too many sources": makeEvidence(MaxEvidenceSources + 1),
		"too many bytes":   {{SourceID: sourceID('a'), Text: strings.Repeat("x", MaxEvidenceBytes)}},
		"empty text":       {{SourceID: sourceID('a'), Text: " \n "}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateInput(invalid); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}

func TestValidateSuggestionsRequiresCitedBoundedKnowledge(t *testing.T) {
	input := []Evidence{{SourceID: sourceID('a'), Text: "Project Atlas is owned by Mina."}}
	valid := []Proposal{{Kind: knowledge.Project, Name: "Project Atlas", Fact: "Mina owns the project.", SourceID: sourceID('a')}}
	if err := ValidateSuggestions(input, valid); err != nil {
		t.Fatalf("valid suggestion rejected: %v", err)
	}
	for name, suggestions := range map[string][]Proposal{
		"uncited":           {{Kind: knowledge.Project, Name: "Project Atlas", Fact: "Mina owns the project.", SourceID: sourceID('b')}},
		"transient task":    {{Kind: knowledge.Project, Name: "Project Atlas", Fact: strings.Repeat("x", MaxSuggestionBytes+1), SourceID: sourceID('a')}},
		"invalid kind":      {{Kind: knowledge.Kind("task"), Name: "Project Atlas", Fact: "Mina owns the project.", SourceID: sourceID('a')}},
		"duplicate":         {valid[0], valid[0]},
		"too many":          makeSuggestions(MaxSuggestions+1, sourceID('a')),
		"untrimmed text":    {{Kind: knowledge.Project, Name: " Project Atlas", Fact: "Mina owns the project.", SourceID: sourceID('a')}},
		"control character": {{Kind: knowledge.Project, Name: "Project Atlas", Fact: "Mina\x00 owns it", SourceID: sourceID('a')}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSuggestions(input, suggestions); err == nil {
				t.Fatal("invalid model suggestion accepted")
			}
		})
	}
}

func makeEvidence(count int) []Evidence {
	items := make([]Evidence, count)
	for i := range items {
		items[i] = Evidence{SourceID: sourceID(byte('a' + i)), Text: "evidence"}
	}
	return items
}

func makeSuggestions(count int, source string) []Proposal {
	items := make([]Proposal, count)
	for i := range items {
		items[i] = Proposal{Kind: knowledge.Topic, Name: "Topic " + string(rune('A'+i)), Fact: "A durable fact.", SourceID: source}
	}
	return items
}
