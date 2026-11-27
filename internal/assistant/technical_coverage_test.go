package assistant

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTechnicalContextCoverageUsesActualClippedPassage(t *testing.T) {
	subjects := []string{"agent_build_01", "agent_build_02"}
	evidence := []Evidence{
		{SourceID: strings.Repeat("a", 64), TechnicalSubjects: subjects[:1], Text: strings.Repeat("unrelated prefix ", 300) + subjects[0]},
		{SourceID: strings.Repeat("b", 64), TechnicalSubjects: subjects[1:], Text: subjects[1] + " finished the parser fix."},
	}
	packed, err := packContextWithOptions("What did both agents each finish?", evidence, nil, 10000, contextOptions{balanced: true, technicalSubjects: subjects})
	if err != nil || len(packed.MissingTechnicalSubjects) != 1 || packed.MissingTechnicalSubjects[0] != subjects[0] || len(packed.TechnicalSources[subjects[0]]) != 0 || len(packed.TechnicalSources[subjects[1]]) != 1 {
		t.Fatalf("unseen tail identifier granted citation coverage: %+v err=%v", packed, err)
	}
	var payload contextPayload
	if err := json.Unmarshal([]byte(packed.User), &payload); err != nil || len(payload.Evidence[0].TechnicalSubjects) != 0 {
		t.Fatalf("clipped source retained a misleading subject label: %+v err=%v", payload, err)
	}
}

func TestTechnicalContextRejectsUnboundedOrInvalidMetadata(t *testing.T) {
	for _, subjects := range [][]string{
		{strings.Repeat("x", MaxQuestionBytes+1)},
		{"agent_build_01", "agent_build_01"},
		{"bad\x00identifier"},
		{""},
	} {
		evidence := []Evidence{{SourceID: strings.Repeat("a", 64), Text: "saved evidence", TechnicalSubjects: subjects}}
		if _, err := packContextWithOptions("question", evidence, nil, 10000, contextOptions{}); err != ErrContext {
			t.Fatalf("invalid evidence metadata accepted: %q err=%v", subjects, err)
		}
		evidence[0].TechnicalSubjects = nil
		if _, err := packContextWithOptions("question", evidence, nil, 10000, contextOptions{technicalSubjects: subjects}); err != ErrContext {
			t.Fatalf("invalid requested metadata accepted: %q err=%v", subjects, err)
		}
	}
}

func TestTechnicalAnswerRequiresEachNamedSubjectAndIndependentCitation(t *testing.T) {
	first, second := strings.Repeat("a", 64), strings.Repeat("b", 64)
	packed := PackedContext{TechnicalSubjects: []string{"agent_build_01", "agent_build_02"}, TechnicalSources: map[string][]string{"agent_build_01": {first}, "agent_build_02": {second}}}
	for _, answer := range []Answer{
		{Text: "agent_build_01 and agent_build_02 finished their tasks.", SourceIDs: []string{first}},
		{Text: "agent_build_01 finished its task.", SourceIDs: []string{first, second}},
	} {
		if technicalAnswerGrounded(answer, packed) {
			t.Fatalf("incomplete comparison accepted: %+v", answer)
		}
	}
	if !technicalAnswerGrounded(Answer{Text: "agent_build_01 finished the parser. agent_build_02 finished the UI.", SourceIDs: []string{first, second}}, packed) {
		t.Fatal("independently cited comparison rejected")
	}
}

func TestTechnicalPassagesValidateOriginalRangesAndNeverJoinIdentifiers(t *testing.T) {
	text := "agent_ MIDDLE build_01"
	evidence := Evidence{SourceID: strings.Repeat("a", 64), Text: text, TechnicalSubjects: []string{"agent_build_01"}, MatchSpans: [][2]int{{0, 6}, {14, len(text)}}}
	packed, err := packContextWithOptions("What did agent_build_01 finish?", []Evidence{evidence}, nil, 10000, contextOptions{technicalSubjects: evidence.TechnicalSubjects})
	if err != nil || len(packed.MissingTechnicalSubjects) != 1 || len(packed.TechnicalSources) != 0 || !strings.Contains(packed.User, "…") {
		t.Fatalf("separate ranges forged a continuous identifier: %+v err=%v", packed, err)
	}
	for name, spans := range map[string][][2]int{
		"negative": {{-1, 2}},
		"overlap":  {{0, 6}, {5, 9}},
		"reversed": {{5, 9}, {0, 3}},
		"outside":  {{0, len(text) + 1}},
		"empty":    {{0, 0}},
	} {
		t.Run(name, func(t *testing.T) {
			evidence.MatchSpans = spans
			if _, err := packContextWithOptions("question", []Evidence{evidence}, nil, 10000, contextOptions{}); err != ErrContext {
				t.Fatalf("invalid original ranges accepted: %v err=%v", spans, err)
			}
		})
	}
	evidence.Text, evidence.MatchSpans = "évidence", [][2]int{{1, 4}}
	if _, err := packContextWithOptions("question", []Evidence{evidence}, nil, 10000, contextOptions{}); err != ErrContext {
		t.Fatal("partial UTF-8 range accepted", err)
	}
	evidence.Text, evidence.MatchSpans = strings.Repeat("x", MaxEvidenceBytes+1), [][2]int{{0, MaxEvidenceBytes + 1}}
	if _, err := packContextWithOptions("question", []Evidence{evidence}, nil, 10000, contextOptions{}); err != ErrContext {
		t.Fatal("oversized combined passage accepted", err)
	}
}

func TestTechnicalIdentifierPresentIgnoresSeparatorSpelling(t *testing.T) {
	for _, text := range []string{"threat-bot runs", "Threat Bot runs", "threat_bot runs", "ThreatBot runs"} {
		if !technicalIdentifierPresent(text, "threat_bot") {
			t.Fatal("separator spelling rejected", text)
		}
	}
	for _, text := range []string{"threatbots runs", "threat_ bot runs", "threat—bot runs", "threat-bot-v2 runs", "threat\nbot runs"} {
		if technicalIdentifierPresent(text, "threat_bot") {
			t.Fatal("different identifier accepted", text)
		}
	}
}
