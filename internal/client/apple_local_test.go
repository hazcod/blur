package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestLocalAppleDayRecapCoversMorningAndAfternoon(t *testing.T) {
	if os.Getenv("BLUR_APPLE_MODEL_INTEGRATION") != "1" {
		t.Skip("requires the installed Apple on-device model and adjacent helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	now := time.Now()
	yesterday := time.Date(now.Year(), now.Month(), now.Day()-1, 0, 0, 0, 0, now.Location())
	var morning, afternoon string
	for i := 0; i < 22; i++ {
		at, text := yesterday.Add(20*time.Hour+time.Duration(i)*time.Minute), fmt.Sprintf("Synthetic only: reviewed Northstar deployment log case %d.", i)
		if i == 0 {
			at, text = yesterday.Add(8*time.Hour), "Synthetic only: investigated the Aurora startup failure in the morning and fixed its stale cache path."
		}
		if i == 1 {
			at, text = yesterday.Add(16*time.Hour), "Synthetic only: finished the Borealis release review in the afternoon and approved its deployment checklist."
		}
		o, err := capture.SanitizeObservation("com.example.recap-fixture", "", text, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			morning = o.ContentID()
		}
		if i == 1 {
			afternoon = o.ContentID()
		}
		if err := store.PutObservationAt(ctx, o, at); err != nil {
			t.Fatal(err)
		}
	}
	noteService := notes.NewService(store, nil, nil)
	controller := newLocalAIController(filepath.Join(t.TempDir(), "ai.json"), noteService)
	defer controller.Close()
	c := NewClient(store).WithNotes(noteService).WithLocalAI(controller)
	defer c.Close()
	c.SetCaptureStateReader(func() (bool, time.Time) { return false, now })
	if err := c.ConfigureLocalAI(ctx, LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	answer, err := c.AskMemory(ctx, "What did I do yesterday?")
	t.Logf("Apple day recap elapsed=%s sent=%d cited=%d limited=%t", time.Since(started), answer.SentItems, len(answer.Sources), answer.CoverageLimited)
	if err != nil || answer.General || answer.Direct || answer.Answer.InsufficientEvidence || !answer.CoverageLimited || !hasRecapSource(answer.Sources, morning) || !hasRecapSource(answer.Sources, afternoon) || !strings.Contains(strings.ToLower(answer.Answer.Text), "aurora") || !strings.Contains(strings.ToLower(answer.Answer.Text), "borealis") {
		t.Fatalf("day recap lost recorded periods: result=%+v err=%v", answer, err)
	}
}

// Compile this test binary beside the bundled helper and explicitly opt in.
// It uses only a temporary encrypted store, never the desktop's user settings.
func TestLocalAppleSavedMemoryWithCapturePaused(t *testing.T) {
	if os.Getenv("BLUR_APPLE_MODEL_INTEGRATION") != "1" {
		t.Skip("requires the installed Apple on-device model and adjacent helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	helper, err := model.NewApple()
	if err != nil {
		t.Fatal(err)
	}
	status, err := helper.Status(ctx)
	if err != nil || !status.Available {
		t.Fatalf("Apple model not admitted: status=%+v err=%v", status, err)
	}
	dir := t.TempDir()
	store, err := durable.OpenDurable(filepath.Join(dir, "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	noteService := notes.NewService(store, nil, nil)
	controller := newLocalAIController(filepath.Join(dir, "local-ai.json"), noteService)
	c := NewClient(store).WithNotes(noteService).WithLocalAI(controller)
	defer c.Close()
	defer controller.Close()
	c.SetCaptureStateReader(func() (bool, time.Time) { return false, time.Now().Add(-time.Hour) })
	note, err := c.AddNote("Synthetic acceptance only: Aurora cache listens on port 4317. The rollback owner is Mira.")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ConfigureLocalAI(ctx, LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}); err != nil {
		t.Fatal(err)
	}
	for _, question := range []string{"What or who is Mira?", "Which port does Aurora use?"} {
		started := time.Now()
		entry, err := c.ResolveEntry(ctx, question, EntryState{}, "UTC")
		if err != nil || entry.Action.Kind != intent.Ask {
			t.Fatal("question interpretation failed", entry.Action.Kind, err)
		}
		answer, err := c.AskMemory(ctx, entry.Action.Text)
		t.Logf("Apple saved-note question completed: elapsed=%s sources=%d generated=%t", time.Since(started), len(answer.Sources), !answer.Direct)
		wantDirect := strings.Contains(question, "Mira") // Identity lookups can quote an exact source without invoking the model.
		if err != nil || answer.Direct != wantDirect || answer.General || answer.Answer.InsufficientEvidence || len(answer.Sources) != 1 || answer.Sources[0].SourceID != note.SourceID {
			t.Fatal("paused generated recall lost original evidence", err)
		}
		want := "4317"
		if strings.Contains(question, "Mira") {
			want = "rollback"
		}
		if !strings.Contains(strings.ToLower(answer.Answer.Text), want) {
			t.Fatal("generated answer missed supplied synthetic fact")
		}
		if strings.Contains(question, "Mira") {
			history := quickHistoryTurns(appendQuickConversation(nil, question, answer))
			follow, err := c.AskMemoryWithHistory(ctx, "Make it shorter", history)
			if err != nil || follow.General || !follow.Direct || follow.ConversationTurns != 1 || follow.Answer.InsufficientEvidence || len(follow.Answer.SourceIDs) != 1 || follow.Answer.SourceIDs[0] != note.SourceID || !strings.Contains(strings.ToLower(follow.Answer.Text), "mira") {
				t.Fatalf("grounded quote shortening lost the saved subject: result=%+v err=%v", follow, err)
			}
		}
	}
	unknown, err := c.AskMemory(ctx, "What is Mira's deployment deadline?")
	if err != nil || !unknown.Answer.InsufficientEvidence {
		t.Errorf("unknown synthetic detail was treated as answered: general=%t sources=%d text=%q err=%v", unknown.General, len(unknown.Sources), unknown.Answer.Text, err)
	}
	general, err := c.askMemoryWithMode(ctx, "Explain SQLite in one short sentence.", nil, "general")
	if err != nil || !general.General || len(general.Sources) != 0 || !strings.Contains(strings.ToLower(general.Answer.Text), "sqlite") {
		t.Fatal("Apple general questions unavailable", err)
	}
	parser, err := intent.NewApple()
	if err != nil {
		t.Fatal(err)
	}
	defer parser.Close()
	action, err := parser.Parse(ctx, intent.Request{Input: "Can you save a note that Aurora uses port 4317?"})
	if err != nil || action.Kind != intent.Note || !strings.Contains(action.Text, "4317") {
		t.Fatal("Apple note interpretation failed", action.Kind, err)
	}
	c.askNow = func() time.Time { return time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC) }
	meeting, err := c.ResolveEntry(ctx, meetingPasteFixture, EntryState{}, "UTC")
	if err != nil || meeting.Action.Kind != intent.Note || meeting.Action.Text != meetingPasteFixture || meeting.Draft != nil {
		t.Fatalf("meeting minutes did not remain one byte-preserved note: action=%+v draft=%+v err=%v", meeting.Action, meeting.Draft, err)
	}
	reminder, err := c.ResolveEntry(ctx, "I need to go to the drycleaners on monday", EntryState{}, "Europe/Brussels")
	local, zoneErr := time.LoadLocation("Europe/Brussels")
	if err != nil || zoneErr != nil || reminder.Action.Kind != intent.Reminder || reminder.Draft == nil || reminder.Draft.Reminder == nil || reminder.Draft.Title != "go to the drycleaners" || reminder.Draft.Reminder.DueAt.In(local).Format("2006-01-02 15:04") != "2026-11-16 09:00" {
		t.Fatalf("natural-language dry-cleaning reminder lost its local time: action=%+v draft=%+v err=%v zoneErr=%v", reminder.Action, reminder.Draft, err, zoneErr)
	}
}
