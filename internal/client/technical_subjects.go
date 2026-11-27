package client

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage"
)

// Check the complete identifier before taking the evidence window. Component
// words in many newer, unrelated labels must not crowd out the named subject.
func (c *Client) filterTechnicalSubjects(ctx context.Context, subjects []string, hits []search.IndexHit) ([]search.IndexHit, bool, error) {
	if len(subjects) == 0 {
		return hits, false, nil
	}
	kept := make([]search.IndexHit, 0, len(hits))
	used, limited := 0, false
	for _, hit := range hits {
		if hit.Bytes < 1 || hit.Bytes > MaxSearchBytes {
			limited = true
			continue
		}
		source, found, err := storage.ReadSourceEvidence(ctx, c.data, hit.SourceID)
		if err != nil {
			return nil, false, err
		}
		if !found {
			continue
		}
		if !technicalSubjectSupport(subjects, source.Text(), source.Title()) {
			continue
		}
		cost := len(source.Text()) + len(source.Title()) + len(source.AppID()) + len(source.Domain())
		if cost > MaxSearchBytes-used {
			limited = true
			continue
		}
		used += cost
		kept = append(kept, hit)
	}
	return kept, limited, nil
}

// Only an explicit comparison asks for independently supported subjects.
// Questions about a relationship between identifiers retain conjunction.
func technicalComparison(plan assistant.Plan) bool {
	return plan.TechnicalComparison && len(plan.TechnicalSubjects) >= 2 && technicalSubjectTermsOnly(plan)
}

// The identifier candidate path derives its query from the identifiers. Keep
// extra literal terms on the ordinary conjunction path so a quoted attribute
// cannot disappear from the required match.
func technicalSubjectTermsOnly(plan assistant.Plan) bool {
	if len(plan.TechnicalSubjects) == 0 || len(plan.TechnicalSubjects) > search.MaxTerms {
		return false
	}
	terms, err := search.NormalizeTokens(strings.Join(plan.TechnicalSubjects, " "), search.MaxTerms)
	if err != nil || len(terms) != len(plan.Terms) {
		return false
	}
	for i := range terms {
		if terms[i] != plan.Terms[i] {
			return false
		}
	}
	return true
}

func technicalEvidenceLimit(plan assistant.Plan) int {
	if technicalComparison(plan) {
		return max(planEvidenceItems, min(len(plan.TechnicalSubjects), assistant.MaxEvidenceItems))
	}
	return planEvidenceItems
}

func (c *Client) technicalComparisonEvidence(ctx context.Context, plan assistant.Plan, bounded bool, windowSegs []activity.Segment, diagnosis *assistant.Diagnosis) ([]assistant.Evidence, bool, error) {
	var evidence []assistant.Evidence
	seen := map[string]int{}
	covered := map[string]bool{}
	used, pending := 0, false
	passageBytes := (assistant.MaxEvidenceBytes - len("\n…\n")*(len(plan.TechnicalSubjects)-1)) / len(plan.TechnicalSubjects)
	for _, subject := range plan.TechnicalSubjects {
		if covered[subject] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, pending, err
		}
		subjectPlan := plan
		subjectPlan.TechnicalSubjects = []string{subject}
		subjectPlan.NumberedSubjects = nil
		var err error
		subjectPlan.Terms, err = search.NormalizeTokens(subject, search.MaxTerms)
		if err != nil {
			return nil, pending, err
		}
		page, err := c.technicalSubjectPage(ctx, subjectPlan, bounded, windowSegs, 1)
		if err != nil {
			return nil, pending, err
		}
		pending = pending || page.IndexingPending
		if diagnosis != nil && (page.HasMore || page.CoverageLimited) {
			diagnosis.SemanticIssue = "limited"
		}
		items, err := c.pageEvidence(ctx, subjectPlan, page, windowSegs)
		if err != nil {
			return nil, pending, err
		}
		for _, item := range items {
			start, end, found := technicalSubjectSpan(subject, item.Text)
			if !found {
				continue
			}
			start = max(0, start-min(32, passageBytes/4))
			for start > 0 && !utf8.RuneStart(item.Text[start]) {
				start--
			}
			end = min(len(item.Text), max(end, start+passageBytes))
			for end < len(item.Text) && end > start && !utf8.RuneStart(item.Text[end]) {
				end--
			}
			item.MatchStart, item.MatchEnd = start, end
			item.MatchSpans = [][2]int{{start, end}}
			item.TermsRelaxed = false
			if index, duplicate := seen[item.SourceID]; duplicate {
				item = evidence[index]
				item.MatchSpans = mergeTechnicalPassages(item.MatchSpans, start, end)
				if technicalPassageBytes(item.MatchSpans) > assistant.MaxEvidenceBytes {
					if diagnosis != nil {
						diagnosis.SemanticIssue = "limited"
					}
					continue
				}
			} else {
				cost := len(item.Text) + len(item.AppID) + len(item.Domain)
				if cost > MaxSearchBytes-used || len(evidence) >= assistant.MaxEvidenceItems {
					if diagnosis != nil {
						diagnosis.SemanticIssue = "limited"
					}
					continue
				}
				used += cost
				seen[item.SourceID] = len(evidence)
			}
			item.TechnicalSubjects = nil
			for _, wanted := range plan.TechnicalSubjects {
				for _, span := range item.MatchSpans {
					if technicalSubjectSupport([]string{wanted}, item.Text[span[0]:span[1]]) {
						item.TechnicalSubjects = append(item.TechnicalSubjects, wanted)
						covered[wanted] = true
						break
					}
				}
			}
			index := seen[item.SourceID]
			if index == len(evidence) {
				evidence = append(evidence, item)
			} else {
				evidence[index] = item
			}
		}
	}
	if diagnosis != nil {
		for _, subject := range plan.TechnicalSubjects {
			if !covered[subject] {
				diagnosis.MissingTechnicalSubjects = append(diagnosis.MissingTechnicalSubjects, subject)
			}
		}
		if len(diagnosis.MissingTechnicalSubjects) > 0 {
			diagnosis.SemanticIssue = "limited"
		}
	}
	return evidence, pending, nil
}

func mergeTechnicalPassages(spans [][2]int, start, end int) [][2]int {
	spans = append(append([][2]int(nil), spans...), [2]int{start, end})
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	merged := spans[:0]
	for _, span := range spans {
		if len(merged) > 0 && span[0] <= merged[len(merged)-1][1] {
			merged[len(merged)-1][1] = max(merged[len(merged)-1][1], span[1])
		} else {
			merged = append(merged, span)
		}
	}
	return merged
}

func technicalPassageBytes(spans [][2]int) int {
	used := max(0, len(spans)-1) * len("\n…\n")
	for _, span := range spans {
		used += span[1] - span[0]
	}
	return used
}

// Locate a whole identifier, separator-insensitively, as byte offsets into the
// immutable UTF-8 original.
func technicalSubjectSpan(subject, text string) (int, int, bool) {
	return search.FindIdentifier(text, subject)
}

func technicalSubjectSupport(subjects []string, fields ...string) bool {
	if len(subjects) > search.MaxTerms {
		return false
	}
	for _, subject := range subjects {
		found := false
		for _, field := range fields {
			if search.ContainsIdentifier(field, subject) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
