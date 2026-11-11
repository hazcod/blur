package chronicle

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestDecodeSummaryResponseAcceptsOnlySourceLinkedFollowUp(t *testing.T) {
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	packed, err := PackHour(Hour{Start: start, End: start.Add(time.Hour), Evidence: []Evidence{evidence(1, start.Add(time.Minute), "meeting discussed launch owners")}})
	if err != nil {
		t.Fatal(err)
	}
	sourceID := packed.SourceIDs[0]
	valid := fmt.Sprintf(`{"text":"Discussed launch ownership.","source_ids":[%q],"follow_up":{"source_id":%q,"text":"Who owns the launch checklist?"}}`, sourceID, sourceID)
	got, err := decodeSummaryResponse(valid, packed)
	if err != nil || got.FollowUp == nil || got.FollowUp.SourceID != sourceID {
		t.Fatalf("valid follow-up response = %+v, %v", got, err)
	}
	withoutQuestion, err := decodeSummaryResponse(fmt.Sprintf(`{"text":"Discussed launch ownership.","source_ids":[%q]}`, sourceID), packed)
	if err != nil || withoutQuestion.FollowUp != nil {
		t.Fatalf("omitted follow-up response = %+v, %v", withoutQuestion, err)
	}
	invalid := fmt.Sprintf(`{"text":"Discussed launch ownership.","source_ids":[%q],"follow_up":{"source_id":%q,"text":"Assign this task"}}`, sourceID, sourceID)
	if _, err := decodeSummaryResponse(invalid, packed); err == nil {
		t.Fatal("non-question follow-up was accepted")
	}
	unknown := fmt.Sprintf(`{"text":"Discussed launch ownership.","source_ids":[%q],"follow_up":{"source_id":%q,"text":"Who owns this?"}}`, sourceID, fmt.Sprintf("%064x", 99))
	if _, err := decodeSummaryResponse(unknown, packed); err == nil {
		t.Fatal("follow-up citation outside the packed evidence was accepted")
	}
}

func TestChroniclePromptAllowsOnlyConservativeFollowUpQuestions(t *testing.T) {
	for _, required := range []string{"optionally one follow_up", "Never create tasks, commitments, reminders, or actions", "meeting", "otherwise omit follow_up"} {
		if !strings.Contains(summarySystemPrompt, required) {
			t.Errorf("summary prompt missing safety rule %q", required)
		}
	}
}
