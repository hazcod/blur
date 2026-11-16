package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/model"
)

func TestAnswerContinuationRoutesToAskWithoutBecomingANote(t *testing.T) {
	c := NewClient(&testData{})
	defer c.Close()
	epoch, _ := c.BeginMemoryRead()
	for _, input := range []string{"Make it shorter", "Make it more formal", "Make it friendlier", "Summarize that", "What is her project?", "What does that mean?"} {
		resolved, err := c.ResolveEntry(context.Background(), input, EntryState{Epoch: epoch, HasAnswer: true}, "UTC")
		if err != nil || resolved.Action.Kind != intent.Ask || resolved.Action.Mode != "memory" || resolved.Action.Text != input {
			t.Fatalf("continuation %q did not stay on the memory answer path: %+v err=%v", input, resolved, err)
		}
	}
	resolved, err := c.ResolveEntry(context.Background(), "Make it shorter", EntryState{Epoch: epoch, HasAnswer: true, AnswerGeneral: true}, "UTC")
	if err != nil || resolved.Action.Kind != intent.Ask || resolved.Action.Mode != "general" {
		t.Fatalf("general continuation crossed into memory: %+v err=%v", resolved, err)
	}
}

func TestEntryErrorsExposeProviderCauseWithoutUnderlyingPrivateText(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{model.ErrAppleUnavailable, "Apple’s on-device model is unavailable"},
		{model.ErrAppleBudget, "context"},
		{context.DeadlineExceeded, "too long"},
		{intent.ErrInvalid, "interpret"},
	} {
		err := errors.Join(tc.err, errors.New("PRIVATE_CAPTURED_TEXT /Users/private token=secret"))
		message := entryInterpretationErrorMessage(err, ProviderApple)
		if !strings.Contains(message, tc.want) || !strings.Contains(message, "Your text") || strings.Contains(message, "PRIVATE") || strings.Contains(message, "token=") {
			t.Fatalf("entry error did not explain its safe cause: %q", message)
		}
	}
}

func TestAnswerContinuationDoesNotOverrideExplicitNoteOrSurviveDeletion(t *testing.T) {
	c := NewClient(&testData{})
	defer c.Close()
	epoch, _ := c.BeginMemoryRead()
	state := EntryState{Epoch: epoch, HasAnswer: true}
	resolved, err := c.ResolveEntry(context.Background(), "note: Make it shorter", state, "UTC")
	if err != nil || resolved.Action.Kind != intent.Note || resolved.Action.Text != "Make it shorter" {
		t.Fatalf("explicit note lost authority: %+v err=%v", resolved, err)
	}
	c.NotifyMemoryChanging()
	c.NotifyMemoryStable()
	if _, err := c.ResolveEntry(context.Background(), "Make it shorter", state, "UTC"); !errors.Is(err, ErrMemoryChanging) {
		t.Fatalf("stale answer context survived a memory mutation: %v", err)
	}
}
