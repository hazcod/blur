package client

import (
	"context"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/storage"
)

// Persistent Ask question history. The durable store keeps the newest 500
// questions (text, asked-at, plan kind, cited source IDs); the quick panel
// loads its recall list from it at startup. It never stores answers, never
// calls a model, and a store without the optional interface simply keeps no
// history.

func planKindLabel(kind assistant.Kind) string {
	switch kind {
	case assistant.KindLookup:
		return "lookup"
	case assistant.KindRecap:
		return "recap"
	case assistant.KindCapture:
		return "capture"
	case assistant.KindTime:
		return "time"
	case assistant.KindEntity:
		return "entity"
	default:
		return "general"
	}
}

// recordAskedQuestion persists one answered question inside the memory
// mutation fence: a deletion that began after the ask started makes this a
// no-op, so a question can never outlive a delete-all that overtook it. The
// store drops cited IDs that no longer resolve, in the same transaction.
// Failure is silent by design; history is a convenience, never the answer.
func (c *Client) recordAskedQuestion(ctx context.Context, epoch uint64, question string, result assistant.Result) {
	history, ok := c.data.(storage.QuestionHistory)
	if !ok || ctx.Err() != nil {
		return
	}
	text := strings.TrimSpace(question)
	if !storage.ValidQuestionText(text) {
		return
	}
	record := storage.QuestionRecord{
		Question:  text,
		AskedAt:   c.askClock().UTC(),
		PlanKind:  planKindLabel(result.Plan.Kind),
		SourceIDs: append([]string(nil), result.Answer.SourceIDs...),
	}
	if len(record.SourceIDs) > storage.MaxQuestionSources {
		record.SourceIDs = record.SourceIDs[:storage.MaxQuestionSources]
	}
	writeCtx, cancel := context.WithTimeout(ctx, shortOperationTimeout)
	defer cancel()
	c.PublishMemoryRead(epoch, func() {
		if publication, ok := result.Publication.(*memoryAnswerPublication); ok && publication.read.fence != nil {
			if writer, ok := publication.read.fence.(storage.QuestionHistoryReadWriter); ok {
				if next, accepted, err := writer.AppendQuestionAtRead(writeCtx, record, publication.read.epoch); err == nil && accepted {
					publication.read.epoch = next
				}
			}
			return
		}
		_ = history.AppendQuestion(writeCtx, record)
	})
}

// RecentQuestions returns up to limit of the newest persisted questions,
// oldest first. It returns nil when the store keeps no history or on error.
func (c *Client) RecentQuestions(ctx context.Context, limit int) []string {
	if c == nil || ctx == nil || limit < 1 {
		return nil
	}
	history, ok := c.data.(storage.QuestionHistory)
	if !ok {
		return nil
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := history.ListQuestions(readCtx, min(limit, storage.MaxQuestionHistory))
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Question)
	}
	return out
}

// restoreQuickHistory replaces the quick panel's recall list with the newest
// persisted questions. It adds no UI; the existing Up/Down/Tab recall simply
// has entries from earlier sessions.
func (d *desktop) restoreQuickHistory() {
	if d == nil {
		return
	}
	d.quickHistory = quickEntryHistory{}
	if d.client == nil {
		return
	}
	for _, question := range d.client.RecentQuestions(context.Background(), quickHistoryLimit) {
		d.quickHistory.Record(question)
	}
}
