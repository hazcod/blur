package assistant

import (
	"context"
	"slices"
	"strings"
)

// CitedTopicMatcher checks current originals without searching or invoking a
// model. A recording's authenticated parent can supply related passages;
// ordinary sources bind only when all attribute terms occur together.
type CitedTopicMatcher interface {
	CitedSourcesMatch(context.Context, []string, []string) (bool, error)
}

func originalSourceFollowup(question string) bool {
	q := strings.ToLower(strings.Trim(strings.TrimSpace(question), ".!?"))
	return q == "show the original" || q == "show the original source" || q == "show original"
}

func (s *Service) citedAttributeFollowup(ctx context.Context, question string, plan Plan, last Turn) (bool, error) {
	if last.General || last.Plan.Kind == KindGeneral || len(last.SourceIDs) == 0 || len(plan.TechnicalSubjects) > 0 || !plan.Scope.IsZero() || len(plan.AppIDs)+len(plan.Domains) > 0 {
		return false, nil
	}
	words := splitWords(question)
	if len(words) < 3 || len(words) > 10 || !hasWord(words[:1], "who", "what") || IsCreationRequest(question) {
		return false, nil
	}
	terms, err := planTerms(question, nil, false)
	if err != nil || len(terms) < 2 || len(terms) > 6 {
		return false, nil
	}
	matcher, ok := s.retriever.(CitedTopicMatcher)
	if !ok {
		return false, nil
	}
	return matcher.CitedSourcesMatch(ctx, last.SourceIDs, terms)
}

func inheritContentContinuation(plan, previous Plan) Plan {
	if plan.EventTime || previous.EventTime || len(plan.Terms) != 0 || len(previous.Terms) == 0 {
		return plan
	}
	plan.Kind, plan.Entity, plan.MatchOnly = previous.Kind, previous.Entity, previous.MatchOnly
	plan.Terms = slices.Clone(previous.Terms)
	if plan.ReferenceQuestion == "" {
		plan.ReferenceQuestion = previous.ReferenceQuestion
		if plan.ReferenceQuestion == "" {
			plan.ReferenceQuestion = previous.Question
		}
	}
	return plan
}
