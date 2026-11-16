package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

// All data and settings are temporary synthetic fixtures; no user store or
// OS notifications are touched. Native delivery is a separate acceptance gate.
func TestLocalIntelligentEntryAndReminderFollowup(t *testing.T) {
	if os.Getenv("BLUR_LOCAL_MODEL_INTEGRATION") != "1" {
		t.Skip("requires a local answer model")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := t.TempDir()
	s, err := durable.OpenDurable(filepath.Join(dir, "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	noteService := notes.NewService(s, nil, nil)
	controller := newLocalAIController(filepath.Join(dir, "local-ai.json"), noteService)
	c := NewClient(s).WithNotes(noteService).WithLocalAI(controller)
	defer controller.Close()
	defer c.Close()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	c.askNow = func() time.Time { return now }
	name := os.Getenv("BLUR_LOCAL_TEST_MODEL")
	if name == "" {
		name = "qwen3:14b"
	}
	if err := c.ConfigureLocalAI(ctx, LocalAISettings{Enabled: true, Provider: ProviderOllama, Endpoint: "http://127.0.0.1:11434", Model: name}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		input string
		kind  intent.Kind
		mode  string
	}{
		{"Write a Go function that reads a file with context cancellation.", intent.Ask, "general"},
		{"Explain my project Atlas.", intent.Ask, "memory"},
		{"I need to review the PR", intent.Task, ""},
		{"The flaky test only fails on ARM runners.", intent.Note, ""},
	} {
		resolved, err := c.ResolveEntry(ctx, test.input, EntryState{}, "UTC")
		if err != nil || resolved.Action.Kind != test.kind || resolved.Action.Mode != test.mode {
			t.Fatalf("%q => %+v %v", test.input, resolved, err)
		}
	}
	resolved, err := c.ResolveEntry(ctx, "Could you schedule a reminder for Friday at nine am to check backups?", EntryState{}, "UTC")
	if err != nil || resolved.Draft == nil || resolved.Draft.Missing != "" || resolved.Action.Kind != intent.Reminder {
		t.Fatal(resolved, err)
	}
	port := &clientNoticePort{}
	service, _ := reminders.NewService(s, port, reminders.Options{Now: func() time.Time { return now }})
	defer service.Close()
	c.reminders = service
	created, err := c.createTaskAtEpoch(ctx, resolved.Draft.Title, resolved.Draft.Reminder, "22222222222222222222222222222222", resolved.Epoch)
	if err != nil || created.Item.Delivery.State != tasks.Scheduled {
		t.Fatal(created, err)
	}
	answer, err := c.AskMemory(ctx, "What should I work on next?")
	if err != nil || answer.Answer.InsufficientEvidence || len(answer.Sources) != 1 || answer.Sources[0].Origin != assistant.OriginTask || len(answer.Targets) != 1 || !strings.Contains(strings.ToLower(answer.Answer.Text), "backups") {
		t.Fatalf("task could not inform a natural personal question: %+v %v", answer, err)
	}
	state := EntryState{Epoch: resolved.Epoch, Targets: []assistant.TargetRef{{Kind: "task", ID: created.Item.ID, Revision: created.Item.Revision, Label: "T1"}}}
	resolved, err = c.ResolveEntry(ctx, "make it ten am instead", state, "UTC")
	if err != nil || resolved.Action.Kind != intent.TaskEdit {
		t.Fatal(resolved, err)
	}
	updated, err := c.ApplyTaskEntry(ctx, resolved, "UTC")
	if err != nil || updated.Draft != nil || updated.Result.Item.Reminder.DueAt.Format(time.RFC3339) != "2026-11-13T10:00:00Z" || len(port.pending) != 1 {
		t.Fatal(updated, err)
	}
	inspected, err := c.ResolveEntry(ctx, "When is it due?", state, "UTC")
	if err != nil || inspected.Answer == nil || !inspected.Answer.Direct || !strings.Contains(inspected.Answer.Answer.Text, "10:00:00") {
		t.Fatal("reminder follow-up lost the current task", inspected, err)
	}
	question := "Write a Go function named Answer returning the integer 42. Return only code."
	first, err := c.AskMemory(ctx, question)
	if err != nil || !first.General || !strings.Contains(first.Answer.Text, "Answer") || !strings.Contains(first.Answer.Text, "42") {
		t.Fatal("general conversation fixture failed", first, err)
	}
	followup, err := c.AskMemoryWithHistory(ctx, "Make it return 43 instead and keep the same function name. Return only the updated code.", []assistant.Turn{{Question: question, Answer: first.Answer.Text, Plan: first.Plan, General: first.General}})
	if err != nil || !followup.General || followup.ConversationTurns != 1 || len(followup.Sources) != 0 || !strings.Contains(followup.Answer.Text, "Answer") || !strings.Contains(followup.Answer.Text, "43") {
		t.Fatal("general follow-up lost the preceding code", followup, err)
	}
}
