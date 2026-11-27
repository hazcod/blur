package assistant

import (
	"errors"
	"strings"
	"testing"
)

func TestGeneratedAbstentionDiscardsKnownCitationsAndModelProse(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, encoded := range []string{
		`{"answer":"Unverified explanation that must never be displayed.","source_labels":["S1"],"insufficient_evidence":true}`,
		`{"answer":"Unverified explanation that must never be displayed.","source_ids":["` + id + `"],"insufficient_evidence":true}`,
	} {
		got, err := parseGeneratedAnswer(encoded, []string{id})
		if err != nil || !got.InsufficientEvidence || got.Text != InsufficientEvidenceText || len(got.SourceIDs) != 0 {
			t.Fatal("model abstention became invalid-response source fallback", got, err)
		}
	}
	_, err := parseGeneratedAnswer(`{"answer":"Not enough evidence.","source_labels":["S9"],"insufficient_evidence":true}`, []string{id})
	if !errors.Is(err, ErrAnswer) {
		t.Fatal("unknown label accepted", err)
	}
}
