package notes

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/capture"
)

func TestMeetingPasteLargerThanIntentBudgetIsStoredWithoutTruncation(t *testing.T) {
	repo := &testRepository{}
	text := "Meeting notes\nTODO: Send the revised plan\n" + strings.Repeat("Decision: retain complete context.\n", 8000) + "Final decision: keep the final line.\n"
	result, err := NewService(repo, failingExtractor{err: ErrModelBudget}, nil).Add(context.Background(), text)
	if err != nil || repo.writeCount != 1 || repo.note.Text() != text || repo.note.Truncated() || result.Processing != ProcessingBudget {
		t.Fatalf("large paste lost bytes: input=%d stored=%d writes=%d truncated=%t state=%s err=%v", len(text), len(repo.note.Text()), repo.writeCount, repo.note.Truncated(), result.Processing, err)
	}
	if len(result.Proposals) != 1 || result.Proposals[0].Confirmed || result.Proposals[0].SourceID != result.SourceID {
		t.Fatalf("budget refusal lost unconfirmed source-linked proposal: %+v", result.Proposals)
	}
}

func TestMeetingPasteRejectsRedactionExpansionRatherThanTruncating(t *testing.T) {
	repo := &testRepository{}
	prefix := "password: x\n"
	text := prefix + strings.Repeat("q", MaxNoteBytes-len(prefix))
	_, err := NewService(repo, nil, nil).Add(context.Background(), text)
	if !errors.Is(err, ErrInvalid) || repo.writeCount != 0 {
		t.Fatalf("redaction expansion silently truncated the note: err=%v writes=%d stored=%d truncated=%t", err, repo.writeCount, len(repo.note.Text()), repo.note.Truncated())
	}
}

func TestMeetingPasteRejectsValidOversizeTextBeforeStorage(t *testing.T) {
	repo := &testRepository{}
	text := strings.Repeat("x", capture.MaxObservationTextBytes+1)
	_, err := NewService(repo, nil, nil).Add(context.Background(), text)
	if !errors.Is(err, ErrInvalid) || repo.writeCount != 0 {
		t.Fatalf("oversize paste persisted partially: err=%v writes=%d", err, repo.writeCount)
	}
}
