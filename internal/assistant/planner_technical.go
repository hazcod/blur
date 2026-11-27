package assistant

import (
	"slices"
	"strings"
	"unicode"
)

// Technical identifiers carry meaningful grammar words (review_long_search,
// retry_policy). Keep the complete label as well as its index tokens.
func technicalSubjects(question string) []string {
	var subjects []string
	for _, field := range strings.Fields(question) {
		field = strings.Trim(field, "!?.,:;()[]{}<>\"'`")
		field = strings.TrimSuffix(strings.TrimSuffix(field, "'s"), "’s")
		if !strings.ContainsRune(field, '_') {
			continue
		}
		valid, word := true, false
		for _, r := range field {
			word = word || unicode.IsLetter(r) || unicode.IsDigit(r)
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("_./\\-", r) {
				valid = false
				break
			}
		}
		if valid && word && !containsString(subjects, field) {
			subjects = append(subjects, field)
		}
	}
	return subjects
}

func maskTechnicalSubjects(question string, subjects []string) string {
	ordered := slices.Clone(subjects)
	slices.SortFunc(ordered, func(a, b string) int { return len(b) - len(a) })
	for _, subject := range ordered {
		question = strings.ReplaceAll(question, subject+"'s", " ")
		question = strings.ReplaceAll(question, subject+"’s", " ")
		question = strings.ReplaceAll(question, subject, " ")
	}
	return question
}

// Only scope/pronoun and generic attribute continuations can carry an older
// identifier. A newly supplied topic or ordinary name must not be overwritten.
func technicalContinuation(plan Plan) bool {
	if technicalSubjectPronoun(plan.Question) {
		return true
	}
	for _, term := range plan.Terms {
		switch term {
		case "agent", "agents", "work", "task", "tasks", "status", "progress", "result", "results", "outcome", "outcomes", "say", "said", "do", "did", "doing", "done":
		default:
			return false
		}
	}
	return true
}

func technicalComparisonQuestion(question string, subjects []string) bool {
	if technicalRelationshipQuestion(question, subjects) {
		return false
	}
	words := splitWords(maskTechnicalSubjects(question, subjects))
	comparison := false
	for _, word := range words {
		switch word.s {
		case "each", "compare", "comparison", "versus", "vs":
			comparison = true
		}
	}
	return comparison
}

func technicalRelationshipQuestion(question string, subjects []string) bool {
	words := splitWords(maskTechnicalSubjects(question, subjects))
	for i, word := range words {
		switch word.s {
		case "call", "calls", "called", "calling", "communicate", "communicates", "communication", "depend", "depends", "dependency", "dependencies", "connect", "connects", "connection", "relationship", "between", "interact", "interaction":
			return true
		case "each":
			if i+1 < len(words) && words[i+1].s == "other" {
				return true
			}
		}
	}
	return false
}

func technicalComparisonContinuation(plan, previous Plan) bool {
	if !previous.TechnicalComparison || len(plan.TechnicalSubjects) > 0 || plan.Entity != "" {
		return false
	}
	words := splitWords(plan.Question)
	if len(words) > 0 && words[0].s == "and" {
		words = words[1:]
	}
	return len(words) >= 2 && words[0].s == "which" && (words[1].s == "one" || words[1].s == "ones")
}

func technicalSubjectPronoun(question string) bool {
	words := splitWords(question)
	if len(words) > 0 && words[0].s == "and" {
		words = words[1:]
	}
	if len(words) == 0 {
		return false
	}
	pronoun := func(word string) bool {
		switch word {
		case "it", "its", "they", "their", "this", "that", "these", "those":
			return true
		}
		return false
	}
	if pronoun(words[0].s) {
		return true
	}
	if len(words) >= 3 && (words[0].s == "what" || words[0].s == "how") && words[1].s == "about" && pronoun(words[2].s) {
		return true
	}
	switch words[0].s {
	case "what", "which", "how", "where", "when", "why", "did", "does", "do", "is", "are", "was", "were", "has", "have", "had", "can", "could", "will", "would", "should":
	default:
		return false
	}
	for i, word := range words {
		switch word.s {
		case "did", "does", "do", "is", "are", "was", "were", "has", "have", "had", "can", "could", "will", "would", "should":
			return i+1 < len(words) && pronoun(words[i+1].s)
		}
	}
	return false
}
