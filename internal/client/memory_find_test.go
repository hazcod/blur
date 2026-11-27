package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/reminders"
)

func TestMemoryFindCommandsStayReadOnlyBeforeTaskInference(t *testing.T) {
	c, _, _, model := singleSearchFixture(t)
	epoch, _ := c.BeginMemoryRead()
	for _, state := range []EntryState{
		{},
		{Epoch: epoch, Pending: &reminders.Draft{Missing: "title"}},
		{Epoch: epoch, HasAnswer: true},
	} {
		for _, tt := range []struct{ input, query string }{
			{"Find silver maple", "silver maple"},
			{"Look for sapphire-4827", "sapphire-4827"},
			{"Search for sapphire-4827", "sapphire-4827"},
			{"Search sapphire-4827", "sapphire-4827"},
			{"find: silver maple", "silver maple"},
			{"search: silver maple", "silver maple"},
			{"search for: silver maple", "silver maple"},
			{"look for: sapphire-4827", "sapphire-4827"},
			{"  LOOK FOR: Sapphire-4827  ", "Sapphire-4827"},
			{"Look for sapphire tomorrow", "sapphire tomorrow"},
			{"Find pending work for Cedar Lantern", "pending work for Cedar Lantern"},
			{"Search outstanding work for Cedar Lantern", "outstanding work for Cedar Lantern"},
		} {
			resolved, err := c.resolveMemoryEntry(context.Background(), tt.input, state, "UTC")
			if err != nil || resolved.SearchQuery != tt.query || resolved.Action.Kind != "" || resolved.Draft != nil {
				t.Errorf("source lookup became an answer/task/draft: %q state=%+v result=%+v err=%v", tt.input, state, resolved, err)
			}
		}
	}
	if model.calls != 0 || model.answerCalls != 0 {
		t.Fatalf("explicit source commands needed model inference: recall=%d answers=%d", model.calls, model.answerCalls)
	}
}

func TestMemoryFindKeepsExplicitTaskAndReminderAuthority(t *testing.T) {
	c, _, _, model := singleSearchFixture(t)
	for _, tt := range []struct {
		input string
		kind  intent.Kind
		text  string
	}{
		{"task: Look for sapphire-4827", intent.Task, "Look for sapphire-4827"},
		{"todo: Find silver maple", intent.Task, "Find silver maple"},
		{"task: Find pending work for Cedar Lantern", intent.Task, "Find pending work for Cedar Lantern"},
		{"note: Find pending work for Cedar Lantern", intent.Note, "Find pending work for Cedar Lantern"},
		{"I need to look for sapphire-4827", intent.Task, "look for sapphire-4827"},
		{"remind me tomorrow to look for sapphire-4827", intent.Reminder, "look for sapphire-4827"},
	} {
		resolved, err := c.resolveMemoryEntry(context.Background(), tt.input, EntryState{}, "UTC")
		if err != nil || resolved.SearchQuery != "" || resolved.Action.Kind != tt.kind || resolved.Action.Text != tt.text {
			t.Errorf("explicit commitment lost its write authority: %q result=%+v err=%v", tt.input, resolved, err)
		}
	}
	if model.calls != 0 || model.answerCalls != 0 {
		t.Fatalf("explicit commitments needed model inference: recall=%d answers=%d", model.calls, model.answerCalls)
	}
}

func TestMemoryFindEmptyCommandsAskForSearchTerms(t *testing.T) {
	c, _, _, model := singleSearchFixture(t)
	for _, input := range []string{"Find", "Find:", "Search", "Search:", "Search for", "Search for:  ", "Look for", "Look for:  "} {
		resolved, err := c.resolveMemoryEntry(context.Background(), input, EntryState{}, "UTC")
		if err != nil || resolved.SearchQuery != "" || resolved.Action.Kind != intent.Clarify || resolved.Message == "" || resolved.Draft != nil {
			t.Errorf("empty source command did not ask for terms: %q result=%+v err=%v", input, resolved, err)
		}
	}
	if model.calls != 0 || model.answerCalls != 0 {
		t.Fatalf("empty source commands needed model inference: recall=%d answers=%d", model.calls, model.answerCalls)
	}
}

// A direct Find command must return indexed sources rather than spend several
// model generations rewriting, ranking task titles, and generating an answer.
func TestMemoryFindReturnsIndexedVoiceWithoutAnswerGeneration(t *testing.T) {
	c, store, _, model := singleSearchFixture(t)
	_, ids := appendArchiveVoice(t, store, "lookup-voice", "Saved personal recording", time.Now().Add(-time.Minute), []string{"The resumed marker is silver maple."})
	resolved, err := c.resolveMemoryEntry(context.Background(), "Find silver maple", EntryState{}, "UTC")
	if err != nil || resolved.SearchQuery != "silver maple" || resolved.Action.Kind != "" {
		t.Fatalf("Find routed to an answer instead of indexed originals: %+v %v", resolved, err)
	}
	page, err := c.SearchMemoryPageContext(context.Background(), resolved.SearchQuery)
	if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != ids[0] || !strings.Contains(page.Hits[0].Text, "silver maple") || model.calls != 0 {
		t.Fatalf("literal indexed voice lookup required generation or lost its original: %+v calls=%d err=%v", page, model.calls, err)
	}
}

func TestMemoryFindKeepsExplicitAnswersAndFactualQuestions(t *testing.T) {
	c, _, _, _ := singleSearchFixture(t)
	for _, input := range []string{"ask: Find silver maple", "ask: Find pending work for Cedar Lantern", "Find out why the release failed", "Find who owns the checklist", "What did I say in my voice note about Cedar Lantern?", "What is pending work for Cedar Lantern?"} {
		resolved, err := c.resolveMemoryEntry(context.Background(), input, EntryState{}, "UTC")
		if err != nil || resolved.SearchQuery != "" || resolved.Action.Kind != intent.Ask {
			t.Fatalf("factual/explicit answer became a keyword lookup: %q %+v %v", input, resolved, err)
		}
	}
}

func TestMemoryFindKeepsTaskCollectionReadsLocal(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	for _, input := range []string{"Show my outstanding tasks", "What's on my plate?"} {
		resolved, err := c.resolveMemoryEntry(context.Background(), input, EntryState{}, "UTC")
		if err != nil || resolved.SearchQuery != "" || resolved.Action.Kind != intent.TaskInspect || resolved.Answer == nil || !resolved.Answer.Direct {
			t.Errorf("task collection request lost its local route: %q result=%+v err=%v", input, resolved, err)
		}
	}
}

func TestMemoryFindOriginalRequestsRetainAnswerContext(t *testing.T) {
	c, _, _, model := singleSearchFixture(t)
	epoch, _ := c.BeginMemoryRead()
	state := EntryState{Epoch: epoch, HasAnswer: true}
	for _, input := range []string{"Show the original", "Show original", "Show the original source."} {
		for _, surface := range []string{"dashboard", "popup"} {
			var resolved EntryResolution
			var err error
			if surface == "dashboard" {
				resolved, err = c.resolveMemoryEntry(context.Background(), input, state, "UTC")
			} else {
				resolved, err = c.ResolveEntry(context.Background(), input, state, "UTC")
			}
			if err != nil || resolved.SearchQuery != "" || resolved.Action.Kind != intent.Ask || resolved.Action.Text != input || resolved.Action.Mode != "memory" {
				t.Errorf("original request lost the preceding answer on %s: %q result=%+v err=%v", surface, input, resolved, err)
			}
		}
	}
	if model.calls != 0 || model.answerCalls != 0 {
		t.Fatalf("original source follow-up needed model inference: recall=%d answers=%d", model.calls, model.answerCalls)
	}
}
