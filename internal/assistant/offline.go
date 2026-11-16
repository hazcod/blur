package assistant

import (
	"context"
	"encoding/json"
	"github.com/hazcod/blur/internal/model"
	"strconv"
	"strings"
)

// quotedEvidenceAnswer exposes saved evidence without pretending to have
// generated a synthesis. It shares the validated, bounded passage selection
// with model answers and keeps complete originals available for inspection.
func quotedEvidenceAnswer(question string, plan Plan, evidence []Evidence, diagnosis Diagnosis, contextLimit int) (Result, error) {
	packed, err := packContextWithOptions(question, evidence, nil, contextLimit-MaxAnswerTokens-PromptTemplateReserve, contextOptions{balanced: plan.Kind == KindRecap, sampled: diagnosis.RecapSampled, chronological: plan.Kind == KindRecap && diagnosis.Chronological})
	if err != nil {
		return Result{}, err
	}
	var payload contextPayload
	if err := json.Unmarshal([]byte(packed.User), &payload); err != nil || len(payload.Evidence) != len(packed.SourceIDs) {
		return Result{}, ErrContext
	}
	byID := make(map[string]Evidence, len(evidence))
	for _, item := range evidence {
		if _, exists := byID[item.SourceID]; !exists {
			byID[item.SourceID] = item
		}
	}
	var text strings.Builder
	voice := len(payload.Evidence) > 0
	for _, item := range payload.Evidence {
		voice = voice && item.AppID == "sh.blur.meetings" && strings.HasPrefix(item.App, "Voice transcript (")
	}
	if voice {
		text.WriteString("Voice transcript:\n\n“")
	} else {
		// Passages that only share some of the question's words are shown as
		// the closest matches, never as if they matched everything asked.
		relaxed := false
		for _, id := range packed.SourceIDs {
			relaxed = relaxed || byID[id].TermsRelaxed
		}
		if relaxed {
			text.WriteString("Saved passages (the closest matches; not every word of your question appears in each):\n\n")
		} else {
			text.WriteString("Saved passages:\n\n")
		}
	}
	sources := make([]Evidence, 0, len(packed.SourceIDs))
	partial := false
	for i, item := range payload.Evidence {
		if voice {
			if i > 0 {
				text.WriteString(" ")
			}
			text.WriteString(item.Text)
		} else {
			if i > 0 {
				text.WriteString("\n\n")
			}
			text.WriteString("[" + strconv.Itoa(i+1) + "] “" + item.Text + "”")
		}
		source, ok := byID[packed.SourceIDs[i]]
		if !ok {
			return Result{}, ErrContext
		}
		sources = append(sources, source)
		partial = partial || source.Partial
	}
	if voice {
		text.WriteString("”")
		if diagnosis.VoiceAssociationUnverified || packed.AssociationUnverified {
			text.WriteString("\n\nI can’t verify which website or video this recording came from.")
		}
	}
	if partial {
		text.WriteString("\n\nSome sources were only partially captured. Open a source to inspect the saved content.")
	}
	answer, err := ValidateAnswer(ModelAnswer{Text: text.String(), SourceIDs: packed.SourceIDs}, packed.SourceIDs)
	if err != nil {
		return Result{}, err
	}
	return Result{Answer: answer, Sources: sources, Direct: true, Plan: plan, IndexingPending: diagnosis.IndexingPending, Omitted: packed.Omitted, Truncated: packed.Truncated, CoverageLimited: memoryCoverageLimited(plan, diagnosis, packed)}, nil
}

func memoryCoverageLimited(plan Plan, diagnosis Diagnosis, packed PackedContext) bool {
	return packed.AssociationUnverified || diagnosis.VoiceAssociationUnverified || diagnosis.VoiceRecallLimited || diagnosis.LibraryRecallLimited || diagnosis.SemanticIssue != "" || packed.Omitted > 0 || packed.Truncated > 0 || plan.Kind == KindRecap && diagnosis.RecapSampled
}

// Never retain or display the rejected model answer. Recovery has no model call
// and uses the same real bounded evidence selection as ordinary offline recall.
func invalidGeneratedEvidence(ctx context.Context, question string, plan Plan, evidence []Evidence, retrieval Retrieval, personal PersonalContext, limit int, attempt PackedContext, inputTokens int) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	result, err := quotedEvidenceAnswer(question, plan, evidence, retrieval.Diagnosis, limit)
	if err != nil {
		return Result{}, err
	}
	result.Answer.Text = "The model answer could not be verified.\n\n" + result.Answer.Text
	result.Answer, err = ValidateAnswer(ModelAnswer{Text: result.Answer.Text, SourceIDs: result.Answer.SourceIDs}, result.Answer.SourceIDs)
	if err != nil {
		return Result{}, err
	}
	result.GenerationIssue = GenerationInvalidResponse
	result.InputTokens = inputTokens
	result.SentItems = len(attempt.SourceIDs)
	result.SentBytes = attempt.InputBytes
	result.AppliedRules = attempt.AppliedRules
	result.PreferencesOmitted = personal.Omitted + attempt.OmittedRules
	result.Targets = citedTargets(result.Answer.SourceIDs, append(append([]TargetRef(nil), personal.Targets...), retrieval.Targets...))
	result.CoverageLimited = result.CoverageLimited || result.Omitted > 0 || result.Truncated > 0
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return result, nil
}

// Only closed structural response failures recover. A wrapped/joined error can
// carry cancellation, consent, deletion or another failure and must propagate.
func invalidGroundedResponse(err error) bool {
	switch err {
	case ErrGeneratorResponse, ErrAnswer, model.ErrAppleResponse, model.ErrOllamaResponse, model.ErrCompatibleResponse, model.ErrAnthropicResponse:
		return true
	default:
		return false
	}
}
