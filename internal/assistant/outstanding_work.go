package assistant

import (
	"strings"

	"github.com/hazcod/blur/internal/knowledge"
)

const possibleOpenPrefix = knowledge.PossibleOpenItemPrefix

// answerOutstandingWork treats project open-work questions as a structured
// read. It uses only saved open-loop proposals and currently open tasks; raw
// page captures cannot establish outstanding work and must not be quoted as a
// fallback when the answer model declines.
func answerOutstandingWork(plan Plan, retrieval Retrieval) (Result, error) {
	var confirmed, possible []string
	var sources []Evidence
	seenLines, seenSources := map[string]bool{}, map[string]bool{}
	addLine := func(dst *[]string, text string, source Evidence) {
		text = strings.TrimSpace(text)
		key := strings.ToLower(strings.Join(strings.Fields(text), " "))
		if key == "" || seenLines[key] {
			return
		}
		seenLines[key] = true
		*dst = append(*dst, text)
		if !seenSources[source.SourceID] {
			seenSources[source.SourceID] = true
			sources = append(sources, source)
		}
	}
	for _, item := range retrieval.Evidence {
		if item.Origin == OriginTask {
			var title string
			var open bool
			for _, line := range strings.Split(item.Text, "\n") {
				line = strings.TrimSpace(line)
				if value, ok := strings.CutPrefix(line, "Saved task: "); ok {
					title = value
				}
				if line == "Current state: open" {
					open = true
				}
			}
			if open && title != "" {
				addLine(&confirmed, title, item)
			}
			continue
		}
		if item.Layer != LayerDigest || item.Origin == OriginUserCorrection || item.Origin == OriginUserFact {
			continue
		}
		for _, line := range strings.Split(item.Text, "\n") {
			line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-• "))
			switch {
			case strings.HasPrefix(line, "Open: "):
				addLine(&confirmed, strings.TrimPrefix(line, "Open: "), item)
			case strings.HasPrefix(line, possibleOpenPrefix):
				addLine(&possible, strings.TrimPrefix(line, possibleOpenPrefix), item)
			}
		}
	}
	if len(confirmed)+len(possible) == 0 {
		var related []Evidence
		for _, item := range retrieval.Evidence {
			if item.Layer == LayerSource && item.Origin == OriginRelatedProject && item.SourceID != "" {
				related = append(related, item)
			}
		}
		if len(related) > 0 {
			return Result{
				Answer: Answer{
					Text:                 "I haven’t found a saved open task for this project yet. I found related project material below that may contain the follow-up.",
					SourceIDs:            evidenceIDs(related),
					InsufficientEvidence: true,
				},
				Sources: related, Direct: true, Plan: plan,
				IndexingPending: retrieval.Diagnosis.IndexingPending,
				CoverageLimited: retrieval.Diagnosis.TaskSearchLimited || retrieval.Diagnosis.SemanticIssue != "",
			}, nil
		}
		answer := Answer{Text: EmptyAnswerText(plan, retrieval.Diagnosis), InsufficientEvidence: true}
		return Result{Answer: answer, Plan: plan, Direct: true, IndexingPending: retrieval.Diagnosis.IndexingPending, CoverageLimited: retrieval.Diagnosis.TaskSearchLimited || retrieval.Diagnosis.SemanticIssue != ""}, nil
	}
	var text strings.Builder
	if len(confirmed) > 0 {
		text.WriteString("Still open:\n")
		for _, line := range confirmed {
			text.WriteString("• " + line + "\n")
		}
	}
	if len(possible) > 0 {
		if text.Len() > 0 {
			text.WriteByte('\n')
		}
		text.WriteString("Possible, not confirmed:\n")
		for _, line := range possible {
			text.WriteString("• " + line + "\n")
		}
	}
	answer, err := ValidateAnswer(ModelAnswer{Text: strings.TrimSpace(text.String()), SourceIDs: evidenceIDs(sources)}, evidenceIDs(sources))
	if err != nil {
		return Result{}, err
	}
	return Result{
		Answer: answer, Sources: sources, Targets: citedTargets(answer.SourceIDs, retrieval.Targets),
		Direct: true, Plan: plan, IndexingPending: retrieval.Diagnosis.IndexingPending,
		CoverageLimited: retrieval.Diagnosis.TaskSearchLimited || retrieval.Diagnosis.SemanticIssue != "",
	}, nil
}

func evidenceIDs(evidence []Evidence) []string {
	ids := make([]string, 0, len(evidence))
	for _, item := range evidence {
		if item.SourceID != "" {
			ids = append(ids, item.SourceID)
		}
	}
	return ids
}
