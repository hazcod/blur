package storage

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

// Persistent Ask question history. It is device-local convenience data: the
// question text, when it was asked, the planner's reading of it and the
// sources the answer cited. It never stores answers.
const (
	// MaxQuestionBytes bounds one stored question (UTF-8 bytes).
	MaxQuestionBytes = 512
	// MaxQuestionHistory is how many of the newest questions are kept.
	MaxQuestionHistory = 500
	// MaxQuestionSources bounds the cited source IDs of one question.
	MaxQuestionSources = 32
	// MaxQuestionPlanKind bounds the plan-kind label.
	MaxQuestionPlanKind = 16
)

// QuestionRecord is one stored Ask question.
type QuestionRecord struct {
	Question  string
	AskedAt   time.Time
	PlanKind  string
	SourceIDs []string // cited sources; the store drops any that do not exist
}

// ValidQuestionText reports whether text may be stored as a question.
func ValidQuestionText(text string) bool {
	return text != "" && len(text) <= MaxQuestionBytes && utf8.ValidString(text) && !strings.ContainsRune(text, 0) && strings.TrimSpace(text) == text
}

// QuestionHistory is the optional durable question history of a store.
type QuestionHistory interface {
	// AppendQuestion stores one question and trims history to the
	// MaxQuestionHistory newest. Cited IDs that are not live sources are dropped.
	AppendQuestion(context.Context, QuestionRecord) error
	// ListQuestions returns up to limit of the newest questions, oldest first.
	ListQuestions(ctx context.Context, limit int) ([]QuestionRecord, error)
	// DeleteAllQuestions clears the whole history.
	DeleteAllQuestions(context.Context) error
}
