package assistant

import (
	"strings"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/search"
)

const passageSeparator = "\n…\n"

// Share the text budget by requested subjects actually present in the selected
// passages. A single source containing nine subjects needs nine shares, rather
// than losing half its passages to a second source containing only one.
func technicalPassageWeight(evidence []Evidence, subjects []string, admitted map[string]struct{}, slots int) (int, int, error) {
	first, total := 0, 0
	seen := make(map[string]bool, len(evidence))
	for i, item := range evidence {
		if i > 0 {
			if _, exists := admitted[item.SourceID]; exists {
				continue
			}
		}
		if seen[item.SourceID] {
			continue
		}
		seen[item.SourceID] = true
		if !validTechnicalSubjects(item.TechnicalSubjects) {
			return 0, 0, ErrContext
		}
		text, err := evidencePassageText(item)
		if err != nil {
			return 0, 0, err
		}
		weight := 0
		for _, subject := range subjects {
			if containsString(item.TechnicalSubjects, subject) && technicalIdentifierPresent(text, subject) {
				weight++
			}
		}
		weight = max(1, weight)
		if i == 0 {
			first = weight
		}
		total += weight
		slots--
		if slots == 0 {
			break
		}
	}
	return first, max(first, total), nil
}

func evidencePassageText(item Evidence) (string, error) {
	if len(item.MatchSpans) == 0 {
		if item.MatchEnd == 0 {
			return item.Text, nil
		}
		if item.MatchStart < 0 || item.MatchEnd <= item.MatchStart || item.MatchEnd > len(item.Text) || !utf8.ValidString(item.Text[item.MatchStart:item.MatchEnd]) {
			return "", ErrContext
		}
		return item.Text[item.MatchStart:item.MatchEnd], nil
	}
	if len(item.MatchSpans) > search.MaxTerms {
		return "", ErrContext
	}
	used, previousEnd := 0, 0
	for i, span := range item.MatchSpans {
		if span[0] < previousEnd || span[1] <= span[0] || span[1] > len(item.Text) || !utf8.ValidString(item.Text[span[0]:span[1]]) {
			return "", ErrContext
		}
		used += span[1] - span[0]
		if i > 0 {
			used += len(passageSeparator)
		}
		if used > MaxEvidenceBytes {
			return "", ErrContext
		}
		previousEnd = span[1]
	}
	var text strings.Builder
	text.Grow(used)
	for i, span := range item.MatchSpans {
		if i > 0 {
			text.WriteString(passageSeparator)
		}
		text.WriteString(item.Text[span[0]:span[1]])
	}
	return text.String(), nil
}

func validTechnicalSubjects(subjects []string) bool {
	if len(subjects) > search.MaxTerms {
		return false
	}
	used := 0
	seen := make(map[string]bool, len(subjects))
	for _, subject := range subjects {
		used += len(subject)
		if subject == "" || used > MaxQuestionBytes || !utf8.ValidString(subject) || strings.ContainsRune(subject, 0) || seen[subject] {
			return false
		}
		seen[subject] = true
	}
	return true
}

func technicalContextSubjects(plan Plan, evidence []Evidence) []string {
	if len(plan.TechnicalSubjects) < 2 {
		return nil
	}
	for _, item := range evidence {
		if len(item.TechnicalSubjects) > 0 {
			return plan.TechnicalSubjects
		}
	}
	return nil
}

func packedTechnicalCoverage(payload contextPayload, ids []string) (map[string][]string, []string) {
	if len(payload.TechnicalSubjects) == 0 {
		return nil, nil
	}
	sources := make(map[string][]string)
	for i := range payload.Evidence {
		item := &payload.Evidence[i]
		var supported []string
		for _, subject := range item.TechnicalSubjects {
			if containsString(payload.TechnicalSubjects, subject) && technicalIdentifierPresent(item.Text, subject) {
				sources[subject] = append(sources[subject], ids[i])
				supported = append(supported, subject)
			}
		}
		item.TechnicalSubjects = supported
	}
	var missing []string
	for _, subject := range payload.TechnicalSubjects {
		if len(sources[subject]) == 0 {
			missing = append(missing, subject)
		}
	}
	return sources, missing
}

// Citation IDs must independently cover every supported requested subject.
// A source's subject labels never grant authority to a different source, and a
// generated claim about an uncovered subject is replaced by the saved quotes.
func technicalAnswerGrounded(answer Answer, packed PackedContext) bool {
	if len(packed.TechnicalSubjects) == 0 || answer.InsufficientEvidence {
		return true
	}
	for _, subject := range packed.MissingTechnicalSubjects {
		if technicalIdentifierPresent(answer.Text, subject) {
			return false
		}
	}
	for subject, ids := range packed.TechnicalSources {
		if !technicalIdentifierPresent(answer.Text, subject) {
			return false
		}
		covered := false
		for _, id := range ids {
			covered = covered || containsString(answer.SourceIDs, id)
		}
		if !covered {
			return false
		}
	}
	return true
}

func technicalCoverageCopy(text string, missing []string) string {
	if len(missing) == 0 {
		return text
	}
	gap := "I couldn’t verify " + strings.Join(missing, ", ") + " from the included passages."
	if text == "" {
		return gap
	}
	return truncateUTF8(text, max(0, MaxAnswerBytes-len(gap)-2)) + "\n\n" + gap
}

// technicalIdentifierPresent shares one separator-insensitive whole-label
// matcher with Ask retrieval so a source and the answer citing it agree.
func technicalIdentifierPresent(text, subject string) bool {
	return search.ContainsIdentifier(text, subject)
}
