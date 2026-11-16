package assistant

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	MaxAnswerBytes           = 8192
	InsufficientEvidenceText = "I don't have enough evidence in the retrieved sources to answer that."
)

var ErrAnswer = errors.New("assistant: invalid model answer")

// ModelAnswer is an untrusted generator result. SourceIDs must be drawn from
// the exact set admitted by PackContext before an answer can be displayed.
type ModelAnswer struct {
	Text                 string   `json:"answer"`
	SourceIDs            []string `json:"source_ids"`
	InsufficientEvidence bool     `json:"insufficient_evidence"`
}

type generatedAnswer struct {
	Text                 string   `json:"answer"`
	SourceLabels         []string `json:"source_labels"`
	SourceIDs            []string `json:"source_ids,omitempty"` // accepted for older providers; never included in the model schema
	InsufficientEvidence bool     `json:"insufficient_evidence"`
}

// Answer is safe to present with source links resolved by the caller.
type Answer struct {
	Text                 string
	SourceIDs            []string
	InsufficientEvidence bool
}

func ValidateAnswer(model ModelAnswer, allowedSourceIDs []string) (Answer, error) {
	return validateAnswerWithLimit(model, allowedSourceIDs, MaxEvidenceItems)
}

func validateAnswerWithLimit(model ModelAnswer, allowedSourceIDs []string, itemLimit int) (Answer, error) {
	if itemLimit < 1 || itemLimit > MaxTranscriptItems || len(model.Text) > MaxAnswerBytes || !utf8.ValidString(model.Text) || strings.ContainsRune(model.Text, 0) || len(allowedSourceIDs) > itemLimit {
		return Answer{}, ErrAnswer
	}
	allowed := make(map[string]struct{}, len(allowedSourceIDs))
	for _, id := range allowedSourceIDs {
		if !validSourceID(id) {
			return Answer{}, ErrAnswer
		}
		allowed[id] = struct{}{}
	}
	if model.InsufficientEvidence {
		if len(model.SourceIDs) != 0 {
			return Answer{}, ErrAnswer
		}
		return Answer{Text: InsufficientEvidenceText, InsufficientEvidence: true}, nil
	}
	text := strings.TrimSpace(model.Text)
	if text == "" || len(model.SourceIDs) == 0 || len(model.SourceIDs) > itemLimit {
		return Answer{}, ErrAnswer
	}
	sources := make([]string, 0, len(model.SourceIDs))
	seen := make(map[string]struct{}, len(model.SourceIDs))
	for _, id := range model.SourceIDs {
		if !validSourceID(id) {
			return Answer{}, ErrAnswer
		}
		if _, ok := allowed[id]; !ok {
			return Answer{}, ErrAnswer
		}
		if _, duplicate := seen[id]; duplicate {
			return Answer{}, ErrAnswer
		}
		seen[id] = struct{}{}
		sources = append(sources, id)
	}
	return Answer{Text: text, SourceIDs: sources}, nil
}
