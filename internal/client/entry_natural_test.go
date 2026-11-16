package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/model/modeltest"
	"github.com/hazcod/blur/internal/notes"
)

func TestModelInterpretedPoliteReminderUsesAuthorizedDateDefault(t *testing.T) {
	input := "Would you help me remember to collect my suit on Monday?"
	server := httptest.NewServer(modeltest.LocalOllama("fixture", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil || len(wire.Messages) != 2 {
			t.Errorf("invalid intent request: %+v, %v", wire, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var request intent.Request
		if err := json.Unmarshal([]byte(wire.Messages[1].Content), &request); err != nil || request.Input != input {
			t.Errorf("intent request lost the explicit user input: %+v, %v", request, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"done":true,"message":{"content":"{\"kind\":\"reminder\",\"text\":\"collect my suit\",\"when\":\"on Monday\"}"}}`))
	})))
	defer server.Close()
	controller := newLocalAIController(filepath.Join(t.TempDir(), "ai.json"), notes.NewService(nil, nil, nil))
	c := NewClient(nil).WithLocalAI(controller)
	defer c.Close()
	defer controller.Close()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	c.askNow = func() time.Time { return now }
	if err := c.ConfigureLocalAI(context.Background(), LocalAISettings{Enabled: true, Provider: ProviderOllama, Endpoint: server.URL, Model: "fixture"}); err != nil {
		t.Fatal(err)
	}
	resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "Europe/Brussels")
	if err != nil || resolved.Action.Kind != intent.Reminder || resolved.Draft == nil || resolved.Draft.Title != "collect my suit" || resolved.Draft.Missing != "" || resolved.Draft.Reminder == nil || resolved.Draft.Reminder.DueAt.Format(time.RFC3339) != "2026-11-16T08:00:00Z" {
		t.Fatalf("grounded model interpretation did not use the authorized clock: %+v, %v", resolved, err)
	}
	resolved, err = c.ResolveEntry(context.Background(), "9am", EntryState{Epoch: resolved.Epoch, Pending: resolved.Draft}, "Europe/Brussels")
	if err != nil || resolved.Draft == nil || resolved.Draft.Missing != "" || resolved.Draft.Reminder == nil || resolved.Draft.Reminder.DueAt.Format(time.RFC3339) != "2026-11-16T08:00:00Z" {
		t.Fatalf("model reminder lost its date during clarification: %+v, %v", resolved, err)
	}
}

func TestNewPersonalCommitmentReplacesPendingReminderInsteadOfBecomingItsClock(t *testing.T) {
	c := NewClient(nil)
	defer c.Close()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	c.askNow = func() time.Time { return now }
	resolved, err := c.ResolveEntry(context.Background(), "remind me tomorrow at 25 to call the dentist", EntryState{}, "UTC")
	if err != nil || resolved.Draft == nil || resolved.Draft.Missing != "time" {
		t.Fatal(resolved, err)
	}
	resolved, err = c.ResolveEntry(context.Background(), "I need to collect my suit on Monday", EntryState{Epoch: resolved.Epoch, Pending: resolved.Draft}, "UTC")
	if err != nil || resolved.Action.Kind != intent.Reminder || resolved.Draft == nil || resolved.Draft.Title != "collect my suit" || resolved.Draft.Missing != "" || resolved.Draft.Reminder == nil {
		t.Fatalf("a new commitment became timing for the previous draft: %+v, %v", resolved, err)
	}
}

func TestNaturalEntryUnderstandsScheduledShorthandWithoutAModel(t *testing.T) {
	c := NewClient(nil)
	defer c.Close()
	c.askNow = func() time.Time { return time.Date(2026, 11, 13, 23, 30, 0, 0, time.UTC) }
	for _, test := range []struct{ input, title, when string }{
		{"monday: get eggs", "get eggs", "monday"},
		{"tomorrow get eggs", "get eggs", "tomorrow"},
		{"on Monday I need to buy eggs", "buy eggs", "Monday"},
		{"Drycleanres on monday", "Drycleanres", "monday"},
	} {
		t.Run(test.input, func(t *testing.T) {
			resolved, err := c.ResolveEntry(context.Background(), test.input, EntryState{}, "Europe/Brussels")
			if err != nil || resolved.Action.Kind != intent.Reminder || resolved.Draft == nil || resolved.Draft.Title != test.title || resolved.Draft.When != test.when || resolved.Draft.Missing != "" || resolved.Draft.Reminder == nil {
				t.Fatalf("common dated task lost its authorized default: %+v, %v", resolved, err)
			}
		})
	}
	resolved, err := c.ResolveEntry(context.Background(), "tomorrow get eggs", EntryState{}, "Europe/Brussels")
	if err != nil || resolved.Draft == nil {
		t.Fatal(resolved, err)
	}
	// 23:30 UTC is already Saturday in Brussels. Tomorrow is Sunday; the
	// follow-up must keep that local date rather than using the UTC calendar.
	resolved, err = c.ResolveEntry(context.Background(), "9am", EntryState{Epoch: resolved.Epoch, Pending: resolved.Draft}, "Europe/Brussels")
	if err != nil || resolved.Draft == nil || resolved.Draft.Reminder == nil || resolved.Draft.Reminder.DueAt.Format(time.RFC3339) != "2026-11-15T08:00:00Z" {
		t.Fatalf("floating day or local timezone was lost during continuation: %+v, %v", resolved, err)
	}
}

func TestNaturalEntryKeepsUndatedCommitmentsAsTasksAndOverridesAsNotes(t *testing.T) {
	c := NewClient(nil)
	defer c.Close()
	for _, test := range []struct {
		input, text string
		kind        intent.Kind
	}{
		{"I need to review the PR", "review the PR", intent.Task},
		{"I've got to collect my prescription", "collect my prescription", intent.Task},
		{"note: monday: get eggs", "monday: get eggs", intent.Note},
		{"note: I need to review the PR", "I need to review the PR", intent.Note},
		{"421-12", "421-12", intent.Ask},
		{"What should I buy on Monday?", "What should I buy on Monday?", intent.Ask},
	} {
		t.Run(test.input, func(t *testing.T) {
			resolved, err := c.ResolveEntry(context.Background(), test.input, EntryState{}, "UTC")
			if err != nil || resolved.Action.Kind != test.kind || resolved.Action.Text != test.text || resolved.Draft != nil {
				t.Fatalf("task/read/note boundary was lost: %+v, %v", resolved, err)
			}
		})
	}
}

func TestNewShorthandTaskReplacesAPendingReminder(t *testing.T) {
	c := NewClient(nil)
	defer c.Close()
	c.askNow = func() time.Time { return time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC) }
	resolved, err := c.ResolveEntry(context.Background(), "remind me tomorrow to call the dentist", EntryState{}, "UTC")
	if err != nil || resolved.Draft == nil {
		t.Fatal(resolved, err)
	}
	for _, input := range []string{"Monday: get eggs", "I need to review the PR"} {
		next, err := c.ResolveEntry(context.Background(), input, EntryState{Epoch: resolved.Epoch, Pending: resolved.Draft}, "UTC")
		if err != nil || next.Action.Text == "call the dentist" || next.Draft != nil && next.Draft.Title != "get eggs" {
			t.Fatalf("new request was consumed as old draft timing: %+v, %v", next, err)
		}
	}
}

func TestFreshActionsAreNotMistakenForEditsToExistingTasks(t *testing.T) {
	c := NewClient(nil)
	defer c.Close()
	c.askNow = func() time.Time { return time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC) }
	for _, test := range []struct{ input, title string }{
		{"finish the tax return tomorrow", "finish the tax return"},
		{"complete the tax return tomorrow", "complete the tax return"},
		{"cancel the streaming subscription on Monday", "cancel the streaming subscription"},
		{"move the bicycle to the shed tomorrow", "move the bicycle to the shed"},
		{"stop for groceries tomorrow", "stop for groceries"},
		{"rename the Downloads folder on Monday", "rename the Downloads folder"},
	} {
		resolved, err := c.ResolveEntry(context.Background(), test.input, EntryState{}, "UTC")
		if err != nil || resolved.Action.Kind != intent.Reminder || resolved.Draft == nil || resolved.Draft.Title != test.title || resolved.Draft.Missing != "" || resolved.Draft.Reminder == nil || resolved.Target != nil {
			t.Errorf("fresh task was hijacked by edit routing: %q => %+v, %v", test.input, resolved, err)
		}
	}
	for _, input := range []string{"cancel that", "complete task 1", "move it to Monday", "rename the task"} {
		resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "UTC")
		if err != nil || resolved.Action.Kind != intent.Clarify || resolved.Target != nil || resolved.Draft != nil {
			t.Errorf("unbound mutation became a new task: %q => %+v, %v", input, resolved, err)
		}
	}
}

func TestCommonUndatedImperativesAreTasksWhileAnswerRequestsStayReadOnly(t *testing.T) {
	c := NewClient(nil)
	defer c.Close()
	for _, test := range []struct {
		input string
		kind  intent.Kind
	}{
		{"buy eggs", intent.Task},
		{"finish the tax return", intent.Task},
		{"cancel the streaming subscription", intent.Task},
		{"read the crash reports", intent.Task},
		{"Write a Go function that adds two integers", intent.Ask},
		{"Explain the crash report", intent.Ask},
		{"Mira owns the rollback", intent.Note},
	} {
		resolved, err := c.ResolveEntry(context.Background(), test.input, EntryState{}, "UTC")
		if err != nil || resolved.Action.Kind != test.kind || resolved.Draft != nil {
			t.Errorf("task/answer/note boundary was lost: %q => %+v, %v", test.input, resolved, err)
		}
	}
}

func TestFutureMutationOfASavedTaskClarifiesInsteadOfExecutingNow(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	created, err := c.CreateTask(context.Background(), "Review the Aurora PR", nil)
	if err != nil {
		t.Fatal(err)
	}
	epoch, _ := c.BeginMemoryRead()
	state := EntryState{Epoch: epoch, Targets: []assistant.TargetRef{{Kind: "task", ID: created.Item.ID, Revision: created.Item.Revision, Label: "T1"}}}
	for _, input := range []string{"complete task 1 tomorrow", "cancel task 1 on Monday", "finish it after lunch"} {
		resolved, err := c.ResolveEntry(context.Background(), input, state, "UTC")
		if err != nil || resolved.Action.Kind != intent.Clarify || resolved.Target != nil || resolved.Draft != nil || resolved.Message == "" {
			t.Errorf("future command became an immediate mutation: %q => %+v, %v", input, resolved, err)
		}
	}
}
