package client

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestVoiceFollowupKeepsAuthenticatedRecordingContext(t *testing.T) {
	c, store, _, _ := singleSearchFixture(t)
	c.localAI = nil
	ctx, now := context.Background(), time.Now()
	c.askNow = func() time.Time { return now }
	_, ids := appendArchiveVoice(t, store, "context-voice", "Launch recording", now.Add(-time.Minute), []string{"Cedar Lantern launch review is Thursday.", "Morgan owns the checklist.", "The reference code is Sapphire4827."})
	appendArchiveVoice(t, store, "yesterday-other-context", "Other recording", now.Add(-24*time.Hour), []string{"Jordan owns the checklist."})
	decoy, _ := capture.SanitizeObservation("com.apple.terminal", "", "Jordan owns the checklist. The reference code is Ruby9932. Cedar Lantern was discussed in Terminal.", 4096)
	if err := store.PutObservationAt(ctx, decoy, now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	question := "What did I say in my voice note about Cedar Lantern?"
	anchor, err := c.AskMemory(ctx, question)
	if err != nil || anchor.Answer.InsufficientEvidence || len(anchor.Sources) != 1 || anchor.Sources[0].SourceID != ids[0] {
		t.Fatal(anchor, err)
	}
	history := quickHistoryTurns(appendQuickConversation(nil, question, anchor))
	for _, terms := range [][]string{{"mina", "patel"}, {"capital", "france"}} {
		matched, err := c.CitedSourcesMatch(ctx, anchor.Answer.SourceIDs, terms)
		if err != nil || matched {
			t.Fatal("recording context hijacked a new subject", terms, matched, err)
		}
	}
	for i, follow := range []string{"Who owns the checklist?", "What was the reference code?", "Show the original"} {
		answer, err := c.AskMemoryWithHistory(ctx, follow, history)
		want := ids[(i+1)%3]
		if err != nil || answer.Answer.InsufficientEvidence || len(answer.Sources) != 1 || answer.Sources[0].SourceID != want {
			t.Fatal("voice context lost", follow, answer, err)
		}
		if i == 0 {
			conversation := appendQuickConversation(nil, question, anchor)
			conversation = appendQuickConversation(conversation, follow, answer)
			dated, err := c.AskMemoryWithHistory(ctx, "And yesterday?", quickHistoryTurns(conversation))
			if err != nil || !dated.Answer.InsufficientEvidence || len(dated.Sources) != 0 {
				t.Fatal("date after attribute escaped recording context", dated, err)
			}
			for _, next := range []string{"And in Terminal?", "And in Terminal yesterday?"} {
				changed, err := c.AskMemoryWithHistory(ctx, next, quickHistoryTurns(conversation))
				if err != nil {
					t.Fatal(changed, err)
				}
				if next == "And in Terminal?" && (len(changed.Sources) != 1 || changed.Sources[0].SourceID != decoy.ContentID()) {
					t.Fatal("explicit app change retained voice route", changed)
				}
				for _, source := range changed.Sources {
					if source.AppID != "com.apple.terminal" {
						t.Fatal("explicit app change admitted recording", source)
					}
				}
			}
		}
	}
	answer, err := c.AskMemoryWithHistory(ctx, "And yesterday?", history)
	if err != nil || !answer.Answer.InsufficientEvidence || len(answer.Sources) != 0 {
		t.Fatal("date followup ignored recording context", answer, err)
	}
	answer, err = c.AskMemory(ctx, "Wat zei ik in mijn spraaknotitie over Cedar Lantern?")
	if err != nil || answer.Answer.InsufficientEvidence || len(answer.Sources) != 1 || answer.Sources[0].SourceID != ids[0] {
		t.Fatal("Dutch voice query lost recording context", answer, err)
	}
}

type limitedVoiceContextRepo struct{ *durable.Store }

func (*limitedVoiceContextRepo) MeetingRecords(context.Context, string, meetings.PageRequest) (meetings.RecordPage, error) {
	return meetings.RecordPage{}, meetings.ErrLimit
}

func TestVoiceContextBudgetDoesNotPermitGlobalFallback(t *testing.T) {
	c, store, _, _ := singleSearchFixture(t)
	c.localAI = nil
	ctx, now := context.Background(), time.Now()
	appendArchiveVoice(t, store, "limited-context-voice", "Launch recording", now.Add(-time.Minute), []string{"Cedar Lantern launch review is Thursday.", "Morgan owns the checklist."})
	decoy, _ := capture.SanitizeObservation("com.apple.terminal", "", "Jordan owns the checklist.", 4096)
	if err := store.PutObservationAt(ctx, decoy, now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	question := "What did I say in my voice note about Cedar Lantern?"
	anchor, err := c.AskMemory(ctx, question)
	if err != nil || anchor.Answer.InsufficientEvidence {
		t.Fatal(anchor, err)
	}
	c.meetings.repo = &limitedVoiceContextRepo{Store: store}
	answer, err := c.AskMemoryWithHistory(ctx, "Who owns the checklist?", quickHistoryTurns(appendQuickConversation(nil, question, anchor)))
	if err != nil || !answer.Answer.InsufficientEvidence || len(answer.Sources) != 0 || !answer.CoverageLimited {
		t.Fatal("limited recording probe became an unrelated answer or hid coverage", answer, err)
	}
}

func TestVoiceAttributeFollowupFindsRecordingTailBeyondPageLimit(t *testing.T) {
	c, store, _, _ := singleSearchFixture(t)
	c.localAI = nil
	ctx, now := context.Background(), time.Now()
	texts := []string{"Cedar Lantern launch review is Thursday."}
	for i := 0; i < 90; i++ {
		texts = append(texts, fmt.Sprintf("Unrelated transcript passage number %d.", i))
	}
	texts = append(texts, "Zara owns the checklist.")
	_, ids := appendArchiveVoice(t, store, "long-context-voice", "Long launch recording", now.Add(-time.Hour), texts)
	decoy, _ := capture.SanitizeObservation("com.apple.terminal", "", "Jordan owns the checklist.", 4096)
	if err := store.PutObservationAt(ctx, decoy, now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	question := "What did I say in my voice note about Cedar Lantern?"
	anchor, err := c.AskMemory(ctx, question)
	if err != nil || anchor.Answer.InsufficientEvidence || len(anchor.Sources) != 1 || anchor.Sources[0].SourceID != ids[0] {
		t.Fatal(anchor, err)
	}
	answer, err := c.AskMemoryWithHistory(ctx, "Who owns the checklist?", quickHistoryTurns(appendQuickConversation(nil, question, anchor)))
	if err != nil || answer.Answer.InsufficientEvidence || len(answer.Sources) != 1 || answer.Sources[0].SourceID != ids[len(ids)-1] {
		t.Fatal("long transcript attribute hidden or broadened", answer, err)
	}
}
