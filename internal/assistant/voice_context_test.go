package assistant

import (
	"encoding/json"
	"strings"
	"testing"
)

func voiceFragments() []Evidence {
	return []Evidence{
		{SourceID: strings.Repeat("a", 64), AppID: "sh.blur.meetings", AppName: "Voice transcript (system)", Text: "TCP congestion", AssociationUnverified: true},
		{SourceID: strings.Repeat("b", 64), AppID: "sh.blur.meetings", AppName: "Voice transcript (system)", Text: "control reduces packet loss.", AssociationUnverified: true},
	}
}

func TestVoiceCandidatePackingKeepsAssociationUnverified(t *testing.T) {
	packed, err := PackContext("What was my YouTube video about?", voiceFragments())
	if err != nil {
		t.Fatal(err)
	}
	var payload contextPayload
	if err := json.Unmarshal([]byte(packed.User), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Evidence) != 2 || !payload.Evidence[0].AssociationUnverified || !payload.Evidence[1].AssociationUnverified || !strings.Contains(packed.System, "not verified") || !strings.Contains(packed.System, "merely mentioned") {
		t.Fatal("voice selection falsely established website/video identity or user activity")
	}
}

func TestVoiceFallbackJoinsSequentialWordsWithoutBrowserNoise(t *testing.T) {
	evidence := voiceFragments()
	result, err := quotedEvidenceAnswer("What was my YouTube video about?", Plan{Kind: KindLookup}, evidence, Diagnosis{VoiceAssociationUnverified: true}, ContextTokenLimit)
	if err != nil || !result.CoverageLimited || len(result.Sources) != 2 || len(result.Answer.SourceIDs) != 2 || !strings.Contains(result.Answer.Text, "TCP congestion control reduces packet loss.") || strings.Contains(result.Answer.Text, "[1]") || strings.Contains(result.Answer.Text, "Matching saved memory") || !strings.Contains(result.Answer.Text, "can’t verify") {
		t.Fatal("voice fallback did not preserve a coherent genuine transcript", err)
	}
	result, err = invalidGeneratedEvidence(t.Context(), "What was my YouTube video about?", Plan{Kind: KindLookup}, evidence, Retrieval{Diagnosis: Diagnosis{VoiceAssociationUnverified: true}}, PersonalContext{}, ContextTokenLimit, PackedContext{SourceIDs: []string{evidence[0].SourceID, evidence[1].SourceID}}, 100)
	if err != nil || result.GenerationIssue != GenerationInvalidResponse || !strings.Contains(result.Answer.Text, "model answer could not be verified") || strings.Contains(result.Answer.Text, "Saved passages are shown below") || strings.Contains(result.Answer.Text, "Saved answers are shown below") {
		t.Fatal("invalid model fallback retained redundant copy or lost uncertainty", err)
	}
}
